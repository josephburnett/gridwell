package urldriver

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// Session is one Chromium tab owned by one URL stream. Its lifetime
// matches the streaming WebSocket: created by Driver.OpenSession, torn
// down by Session.Close. State is per-session — multiple sessions to
// the same (userID, tileID) are independent tabs.
//
// Frames() and Navs() return the read ends of unbounded-ish channels
// the caller drains. Done() unblocks once the session is fully torn
// down (either via Close or because the underlying chromedp tab
// context died on its own).
type Session struct {
	userID, tileID int64

	ctx    context.Context // chromedp tab ctx
	cancel context.CancelFunc

	streamInterval time.Duration

	frames chan []byte
	navs   chan string

	stopFrames chan struct{}
	framesDone chan struct{}

	closeOnce sync.Once
	done      chan struct{}

	mu      sync.Mutex
	lastURL string
}

// OpenSession spawns a Chromium tab for (userID, tileID), navigates to
// initialURL, and sets the viewport to w×h. Returns a Session whose
// Frames()/Navs() channels deliver activity until Close().
//
// Multiple OpenSession calls for the same (userID, tileID) produce
// independent tabs that share the user's Chromium process.
func (d *Driver) OpenSession(userID, tileID int64, initialURL string, w, h int64) (*Session, error) {
	if !d.available {
		return nil, ErrUnavailable
	}
	if w <= 0 {
		w = d.cfg.ViewportWidth
	}
	if h <= 0 {
		h = d.cfg.ViewportHeight
	}
	d.mu.Lock()
	browser, err := d.userBrowserLocked(userID)
	d.mu.Unlock()
	if err != nil {
		return nil, err
	}

	tabCtx, cancel := chromedp.NewContext(browser.ctx)
	s := &Session{
		userID:         userID,
		tileID:         tileID,
		ctx:            tabCtx,
		cancel:         cancel,
		streamInterval: d.cfg.StreamInterval,
		// Frames cap=4 with newest-wins drop policy. Stale UI frames
		// in flight waste bandwidth more than they help.
		frames:     make(chan []byte, 4),
		navs:       make(chan string, 8),
		stopFrames: make(chan struct{}),
		framesDone: make(chan struct{}),
		done:       make(chan struct{}),
		lastURL:    initialURL,
	}

	// If the per-tab chromedp ctx dies for any reason (CDP target
	// destroyed, browser process exit, etc.), unblock Done(). Close()
	// itself is idempotent via closeOnce.
	go func() {
		<-tabCtx.Done()
		log.Printf("[urldriver] session tab ctx done uid=%d tile=%d err=%v",
			userID, tileID, tabCtx.Err())
		s.Close()
	}()

	s.installListeners()

	if err := runWithTimeout(tabCtx, 20*time.Second,
		emulation.SetDeviceMetricsOverride(w, h, 1.0, false),
		injectHardening(),
		chromedp.Navigate(initialURL),
	); err != nil {
		log.Printf("[urldriver] session init failed uid=%d tile=%d err=%v",
			userID, tileID, err)
		// Proceed: the watchdog will tear down if the ctx is dead;
		// otherwise the session is recoverable (a failed Navigate can
		// be followed by a different click/nav from the user).
	}

	go s.frameLoop()
	return s, nil
}

// installListeners hooks dialog auto-dismiss + main-frame navigation
// tracking. chromedp's ListenTarget callback runs on its own goroutine;
// we never block in it.
func (s *Session) installListeners() {
	chromedp.ListenTarget(s.ctx, func(ev any) {
		switch e := ev.(type) {
		case *page.EventJavascriptDialogOpening:
			go func() {
				_ = runWithTimeout(s.ctx, 2*time.Second,
					page.HandleJavaScriptDialog(false))
			}()
		case *page.EventFrameNavigated:
			if e.Frame == nil || string(e.Frame.ParentID) != "" {
				return
			}
			u := e.Frame.URL
			if !schemeAllowed(u) {
				return
			}
			s.mu.Lock()
			s.lastURL = u
			s.mu.Unlock()
			select {
			case s.navs <- u:
			default:
				// Drop on overflow; the next nav will overwrite.
			}
		}
	})
}

// frameLoop polls the tab at streamInterval, pushing JPEGs onto
// s.frames. Exits when stopFrames is closed or the tab ctx dies.
func (s *Session) frameLoop() {
	defer close(s.framesDone)
	timer := time.NewTimer(s.streamInterval)
	defer timer.Stop()
	for {
		select {
		case <-s.stopFrames:
			return
		case <-s.ctx.Done():
			return
		case <-timer.C:
		}
		var buf []byte
		if err := runWithTimeout(s.ctx, s.streamInterval+1*time.Second,
			captureJPEG(&buf)); err == nil && len(buf) > 0 {
			s.pushFrame(buf)
		}
		timer.Reset(s.streamInterval)
	}
}

// pushFrame inserts buf into s.frames, dropping the oldest if full.
func (s *Session) pushFrame(buf []byte) {
	for {
		select {
		case s.frames <- buf:
			return
		default:
			select {
			case <-s.frames: // drop oldest, retry
			default:
				return
			}
		}
	}
}

// Input synchronously dispatches a single input event to the tab.
// Caller (the WS reader goroutine) is naturally rate-limited by the
// network round-trip, so we don't buffer.
func (s *Session) Input(ev InputEvent) error {
	action, err := inputAction(ev)
	if err != nil {
		return err
	}
	return runWithTimeout(s.ctx, 2*time.Second, action)
}

// Resize sets the device viewport to (w, h). The page reflows on the
// next render frame.
func (s *Session) Resize(w, h int64) error {
	if w <= 0 || h <= 0 {
		return fmt.Errorf("invalid resize %dx%d", w, h)
	}
	return runWithTimeout(s.ctx, 2*time.Second,
		emulation.SetDeviceMetricsOverride(w, h, 1.0, false))
}

// LastURL returns the most recently observed main-frame URL. Initial
// value is the URL passed to OpenSession; updated as
// Page.frameNavigated events fire.
func (s *Session) LastURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastURL
}

// Frames returns the read end of the frame channel.
func (s *Session) Frames() <-chan []byte { return s.frames }

// Navs returns the read end of the navigation channel.
func (s *Session) Navs() <-chan string { return s.navs }

// Done is closed when the session has been fully torn down.
func (s *Session) Done() <-chan struct{} { return s.done }

// CaptureFinal grabs one last screenshot. Intended to be called once
// on WS close, before Close. After Close the tab ctx is canceled and
// CaptureFinal returns a CDP error.
//
// ctx is reserved for future use as a caller-controllable deadline; v1
// uses a fixed 3s timeout on the tab ctx.
func (s *Session) CaptureFinal(_ context.Context) ([]byte, error) {
	var buf []byte
	if err := runWithTimeout(s.ctx, 3*time.Second, captureJPEG(&buf)); err != nil {
		return nil, err
	}
	return buf, nil
}

// Close tears down the session: stops the frame loop, destroys the
// Chromium tab. Idempotent.
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		close(s.stopFrames)
		<-s.framesDone
		s.cancel()
		close(s.done)
	})
}
