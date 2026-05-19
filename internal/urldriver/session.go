//go:build !js

package urldriver

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// Session is one Chromium tab owned by one URL stream. Lifetime matches
// the streaming WebSocket: created by Driver.OpenSession, torn down by
// Session.Close.
//
// rod's *Page survives cross-process renderer swaps (the whole reason
// we picked rod over chromedp). Session.Done() therefore only fires on
// genuine page death: the underlying CDP target was detached or
// destroyed (e.g., renderer crash, programmatic close).
type Session struct {
	userID, tileID int64

	// page is the live tab. pageCancel cancels the page's context,
	// which is the signal that wakes our EachEvent goroutines and the
	// frameLoop. We derive page via Browser.Page → WithCancel so we
	// own the cancellation.
	page       *rod.Page
	pageCancel context.CancelFunc

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
// initialURL, and sets the viewport to w×h. Frames()/Navs() deliver
// activity until Close(). Multiple OpenSession calls for the same
// (userID, tileID) produce independent tabs.
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

	// Create a fresh tab. rod's Page() uses Target.AttachToTarget with
	// Flatten:true under the hood; the resulting *Page rebinds the
	// underlying CDP session on cross-process target swaps.
	rawPage, err := browser.browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return nil, fmt.Errorf("create page: %w", err)
	}

	// Derive a cancellable page so we can stop our event subscriptions
	// on Close. The cancel is independent of the underlying CDP
	// target; cancelling it does NOT close the tab, only our
	// goroutines watching it.
	page, cancel := rawPage.WithCancel()

	s := &Session{
		userID:         userID,
		tileID:         tileID,
		page:           page,
		pageCancel:     cancel,
		streamInterval: d.cfg.StreamInterval,
		// Frame channel uses newest-wins drop in pushFrame.
		frames:     make(chan []byte, 4),
		navs:       make(chan string, 8),
		stopFrames: make(chan struct{}),
		framesDone: make(chan struct{}),
		done:       make(chan struct{}),
		lastURL:    initialURL,
	}

	// Watchdog: page ctx is canceled by rod on
	// Target.targetDetachedFromTarget or Target.targetTargetDestroyed
	// matching this page's target ID. Cross-process navigations swap
	// the underlying target but the *Page survives — this fires only
	// on genuine death. (Renderer crashes via Target.targetCrashed
	// require an explicit subscription; see crashWatch below.)
	go func() {
		<-rawPage.GetContext().Done()
		log.Printf("[urldriver] page ctx done uid=%d tile=%d err=%v",
			userID, tileID, rawPage.GetContext().Err())
		s.Close()
	}()

	s.installListeners(browser.browser, rawPage.TargetID)

	// Viewport + injected hardening JS, then navigate. Errors are
	// logged and swallowed: a failed initial nav still leaves a
	// usable Session the client can drive (e.g. reload via the URL
	// bar — actually no, we don't have a URL bar, but the WS can
	// dispatch keys / mouse). Best effort.
	if err := page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
		Width: int(w), Height: int(h), DeviceScaleFactor: 1,
	}); err != nil {
		log.Printf("[urldriver] set viewport failed uid=%d tile=%d err=%v",
			userID, tileID, err)
	}
	if _, err := page.EvalOnNewDocument(hardeningJS); err != nil {
		log.Printf("[urldriver] inject hardening failed uid=%d tile=%d err=%v",
			userID, tileID, err)
	}
	if err := page.Timeout(20 * time.Second).Navigate(initialURL); err != nil {
		log.Printf("[urldriver] navigate failed uid=%d tile=%d err=%v",
			userID, tileID, err)
	}

	go s.frameLoop()
	return s, nil
}

// installListeners hooks dialog auto-dismiss, main-frame navigation
// tracking, and renderer-crash detection. All subscriptions run on
// goroutines that exit when s.pageCancel is invoked by Close.
func (s *Session) installListeners(browser *rod.Browser, targetID proto.TargetTargetID) {
	// Dialog auto-dismiss.
	go s.page.EachEvent(func(e *proto.PageJavascriptDialogOpening) {
		// Accept=false → cancel the dialog.
		_ = proto.PageHandleJavaScriptDialog{Accept: false}.Call(s.page)
	})()

	// Main-frame navigation tracking. Sub-frame navs (ads, embeds)
	// don't rename the tile.
	go s.page.EachEvent(func(e *proto.PageFrameNavigated) {
		if e.Frame == nil || e.Frame.ParentID != "" {
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
		}
	})()

	// Renderer-crash watchdog. rod does NOT auto-cancel the page ctx
	// on Target.targetCrashed, so we subscribe at the browser level
	// and tear ourselves down on a match.
	go browser.EachEvent(func(e *proto.TargetTargetCrashed) {
		if e.TargetID != targetID {
			return
		}
		log.Printf("[urldriver] page crashed uid=%d tile=%d status=%s errorCode=%d",
			s.userID, s.tileID, e.Status, e.ErrorCode)
		s.Close()
	})()
}

// frameLoop polls the tab at streamInterval, pushing JPEGs onto
// s.frames. Exits when stopFrames is closed or the page ctx dies.
func (s *Session) frameLoop() {
	defer close(s.framesDone)
	quality := 70
	timer := time.NewTimer(s.streamInterval)
	defer timer.Stop()
	for {
		select {
		case <-s.stopFrames:
			return
		case <-s.page.GetContext().Done():
			return
		case <-timer.C:
		}
		buf, err := s.page.Timeout(s.streamInterval+1*time.Second).
			Screenshot(false, &proto.PageCaptureScreenshot{
				Format:  proto.PageCaptureScreenshotFormatJpeg,
				Quality: &quality,
			})
		if err == nil && len(buf) > 0 {
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

// Input synchronously dispatches one input event to the page via raw
// CDP. Uses proto.* directly (instead of page.Mouse / page.Keyboard
// helpers) so explicit per-event modifier bits and X/Y are honored.
func (s *Session) Input(ev InputEvent) error {
	page := s.page.Timeout(2 * time.Second)
	mods := int(ev.Modifiers)
	switch ev.Kind {
	case InputMouseMove:
		return proto.InputDispatchMouseEvent{
			Type:      proto.InputDispatchMouseEventTypeMouseMoved,
			X:         ev.X, Y: ev.Y,
			Modifiers: mods,
		}.Call(page)
	case InputMouseDown:
		return proto.InputDispatchMouseEvent{
			Type:       proto.InputDispatchMouseEventTypeMousePressed,
			X:          ev.X, Y: ev.Y,
			Button:     rodMouseButton(ev.Button),
			ClickCount: 1,
			Modifiers:  mods,
		}.Call(page)
	case InputMouseUp:
		return proto.InputDispatchMouseEvent{
			Type:       proto.InputDispatchMouseEventTypeMouseReleased,
			X:          ev.X, Y: ev.Y,
			Button:     rodMouseButton(ev.Button),
			ClickCount: 1,
			Modifiers:  mods,
		}.Call(page)
	case InputMouseWheel:
		return proto.InputDispatchMouseEvent{
			Type:      proto.InputDispatchMouseEventTypeMouseWheel,
			X:         ev.X, Y: ev.Y,
			DeltaY:    ev.DeltaY,
			Modifiers: mods,
		}.Call(page)
	case InputKeyDown:
		return proto.InputDispatchKeyEvent{
			Type:      proto.InputDispatchKeyEventTypeKeyDown,
			Key:       ev.Key,
			Code:      ev.Code,
			Modifiers: mods,
		}.Call(page)
	case InputKeyUp:
		return proto.InputDispatchKeyEvent{
			Type:      proto.InputDispatchKeyEventTypeKeyUp,
			Key:       ev.Key,
			Code:      ev.Code,
			Modifiers: mods,
		}.Call(page)
	case InputResize:
		return s.Resize(ev.Width, ev.Height)
	default:
		return fmt.Errorf("unknown input kind %q", ev.Kind)
	}
}

// Resize sets the device viewport to w×h. The page reflows on the
// next render frame.
func (s *Session) Resize(w, h int64) error {
	if w <= 0 || h <= 0 {
		return fmt.Errorf("invalid resize %dx%d", w, h)
	}
	return s.page.Timeout(2 * time.Second).SetViewport(&proto.EmulationSetDeviceMetricsOverride{
		Width: int(w), Height: int(h), DeviceScaleFactor: 1,
	})
}

// LastURL returns the most recently observed main-frame URL.
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
// on WS close, before Close. After Close the page ctx is canceled and
// this returns a CDP error.
//
// ctx is reserved for future use as a caller-controllable deadline;
// v1 uses a fixed 3s timeout on the page.
func (s *Session) CaptureFinal(_ context.Context) ([]byte, error) {
	quality := 70
	return s.page.Timeout(3 * time.Second).Screenshot(false, &proto.PageCaptureScreenshot{
		Format:  proto.PageCaptureScreenshotFormatJpeg,
		Quality: &quality,
	})
}

// Close tears down the session: stops the frame loop, cancels event
// goroutines, closes the Chromium tab. Idempotent.
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		close(s.stopFrames)
		<-s.framesDone
		s.pageCancel() // stop EachEvent goroutines
		// page.Close sends Page.close — gracefully closes the tab.
		// Errors here are expected if the tab is already gone
		// (renderer crash, target destroyed); ignore them.
		_ = s.page.Close()
		close(s.done)
	})
}

func rodMouseButton(name string) proto.InputMouseButton {
	switch name {
	case MouseButtonLeft:
		return proto.InputMouseButtonLeft
	case MouseButtonMiddle:
		return proto.InputMouseButtonMiddle
	case MouseButtonRight:
		return proto.InputMouseButtonRight
	default:
		return proto.InputMouseButtonNone
	}
}
