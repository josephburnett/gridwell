//go:build !js

package urldriver

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// Session is one Chromium tab owned by one URL stream.
//
// The "tab" follows clicks across new-tab boundaries: when the current page
// opens a new tab (target="_blank", window.open, middle-click), the Session
// attaches to the new tab and closes the old one. From the user's
// perspective the URL tile is always "where I just clicked to."
type Session struct {
	tileID int64

	browser *rod.Browser

	streamInterval time.Duration

	frames chan []byte
	navs   chan string

	stopFrames chan struct{}
	framesDone chan struct{}

	closeOnce sync.Once
	done      chan struct{}

	mu              sync.Mutex
	lastURL         string
	page            *rod.Page          // current tab, derived via WithCancel
	pageCancel      context.CancelFunc // cancels page-level listeners
	currentTargetID proto.TargetTargetID
	// Most recent viewport set via Resize / Input(InputResize) / OpenSession.
	// Replayed after every tab swap so the new tab matches the tile size.
	viewportW, viewportH int64
}

// OpenSession spawns a Chromium tab for tileID, navigates to initialURL,
// and sets the viewport to w×h.
func (d *Driver) OpenSession(tileID int64, initialURL string, w, h int64) (*Session, error) {
	if w <= 0 {
		w = d.cfg.ViewportWidth
	}
	if h <= 0 {
		h = d.cfg.ViewportHeight
	}
	d.mu.Lock()
	browser, err := d.ensureBrowserLocked()
	d.mu.Unlock()
	if err != nil {
		return nil, err
	}

	rawPage, err := browser.browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return nil, fmt.Errorf("create page: %w", err)
	}

	page, cancel := rawPage.WithCancel()

	s := &Session{
		tileID:          tileID,
		browser:         browser.browser,
		streamInterval:  d.cfg.StreamInterval,
		frames:          make(chan []byte, 4),
		navs:            make(chan string, 8),
		stopFrames:      make(chan struct{}),
		framesDone:      make(chan struct{}),
		done:            make(chan struct{}),
		lastURL:         initialURL,
		page:            page,
		pageCancel:      cancel,
		currentTargetID: rawPage.TargetID,
		viewportW:       w,
		viewportH:       h,
	}

	// Browser-level listeners survive page swaps and are installed once.
	s.installBrowserListeners()
	// Page-level listeners are tied to a specific *rod.Page; reinstalled
	// after every swap.
	s.installPageListeners()

	if err := page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
		Width: int(w), Height: int(h), DeviceScaleFactor: 1,
	}); err != nil {
		log.Printf("[urldriver] set viewport failed tile=%d err=%v", tileID, err)
	}
	if _, err := page.EvalOnNewDocument(hardeningJS); err != nil {
		log.Printf("[urldriver] inject hardening failed tile=%d err=%v", tileID, err)
	}
	if err := page.Timeout(20 * time.Second).Navigate(initialURL); err != nil {
		log.Printf("[urldriver] navigate failed tile=%d err=%v", tileID, err)
	}

	go s.frameLoop()
	return s, nil
}

// installBrowserListeners subscribes to browser-level CDP events that we
// care about across the lifetime of this Session, regardless of which tab
// the session is currently bound to.
func (s *Session) installBrowserListeners() {
	// New tab opened by our current page (target="_blank", window.open,
	// middle-click). Follow it.
	go s.browser.EachEvent(func(e *proto.TargetTargetCreated) {
		if e.TargetInfo == nil || e.TargetInfo.Type != "page" {
			return
		}
		s.mu.Lock()
		currentID := s.currentTargetID
		s.mu.Unlock()
		if e.TargetInfo.OpenerID != currentID {
			return
		}
		s.swapToTarget(e.TargetInfo.TargetID, e.TargetInfo.URL)
	})()

	// Current tab destroyed (renderer gone, external Close.target call).
	// Old tabs we close ourselves during a swap are filtered out because
	// we update currentTargetID before issuing the close.
	go s.browser.EachEvent(func(e *proto.TargetTargetDestroyed) {
		s.mu.Lock()
		currentID := s.currentTargetID
		s.mu.Unlock()
		if e.TargetID != currentID {
			return
		}
		log.Printf("[urldriver] current target destroyed tile=%d", s.tileID)
		s.Close()
	})()

	// Renderer crash on the current tab.
	go s.browser.EachEvent(func(e *proto.TargetTargetCrashed) {
		s.mu.Lock()
		currentID := s.currentTargetID
		s.mu.Unlock()
		if e.TargetID != currentID {
			return
		}
		log.Printf("[urldriver] page crashed tile=%d status=%s errorCode=%d",
			s.tileID, e.Status, e.ErrorCode)
		s.Close()
	})()

	// Browser process death: tear ourselves down so the WS handler
	// observes our Done() and reports the failure to the client.
	go func() {
		<-s.browser.GetContext().Done()
		log.Printf("[urldriver] browser ctx done tile=%d", s.tileID)
		s.Close()
	}()
}

// installPageListeners subscribes to events on s.page. Must be re-called
// after every page swap; the old goroutines exit when the prior page's
// context is canceled.
func (s *Session) installPageListeners() {
	s.mu.Lock()
	page := s.page
	s.mu.Unlock()

	go page.EachEvent(func(e *proto.PageJavascriptDialogOpening) {
		_ = proto.PageHandleJavaScriptDialog{Accept: false}.Call(page)
	})()

	go page.EachEvent(func(e *proto.PageFrameNavigated) {
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
}

// swapToTarget retargets the Session at newTargetID, closes the previous
// tab, and re-installs page-level listeners on the new page.
//
// initialURL is taken from the TargetInfo and used as the immediate
// lastURL until PageFrameNavigated catches up — without this, a fast
// WS-close after a "click _blank → done" sequence would persist the
// stale source URL on the tile.
func (s *Session) swapToTarget(newTargetID proto.TargetTargetID, initialURL string) {
	newRaw, err := s.browser.PageFromTarget(newTargetID)
	if err != nil {
		log.Printf("[urldriver] page from target %s tile=%d: %v", newTargetID, s.tileID, err)
		return
	}
	newPage, newCancel := newRaw.WithCancel()

	s.mu.Lock()
	oldCancel := s.pageCancel
	oldTargetID := s.currentTargetID
	s.page = newPage
	s.pageCancel = newCancel
	s.currentTargetID = newTargetID
	if schemeAllowed(initialURL) {
		s.lastURL = initialURL
	}
	vw, vh := s.viewportW, s.viewportH
	s.mu.Unlock()

	// Apply the current viewport before anything else: the new tab is
	// born at Chromium's default and a tile size mismatch shows up as a
	// stretched / letterboxed JPEG until the user reloads.
	if vw > 0 && vh > 0 {
		if err := newPage.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
			Width: int(vw), Height: int(vh), DeviceScaleFactor: 1,
		}); err != nil {
			log.Printf("[urldriver] viewport on new tab tile=%d: %v", s.tileID, err)
		}
	}

	// Inject hardening into the new tab. The initial nav of the new tab
	// is already in flight and may not see this, but any subsequent
	// in-tab nav will. Best-effort.
	if _, err := newPage.EvalOnNewDocument(hardeningJS); err != nil {
		log.Printf("[urldriver] hardening on new tab tile=%d: %v", s.tileID, err)
	}

	// Wire up dialog + nav listeners on the new page first, THEN cancel
	// the old listeners. Reversing the order would create a brief window
	// where in-flight events on the old page get dispatched but nothing
	// is listening for the equivalents on the new page.
	s.installPageListeners()
	oldCancel()

	if _, err := (proto.TargetCloseTarget{TargetID: oldTargetID}).Call(s.browser); err != nil {
		log.Printf("[urldriver] close old target %s tile=%d: %v", oldTargetID, s.tileID, err)
	}

	if schemeAllowed(initialURL) {
		select {
		case s.navs <- initialURL:
		default:
		}
	}

	// The TargetCreated event often carries url="" because Chromium
	// creates the target before navigating. Our PageFrameNavigated
	// listener can also miss the initial navigation if it fired before
	// we subscribed (a race we can't tighten without losing events
	// on real swaps). Poll the new page's TargetInfo briefly so the
	// tile reflects the destination URL even when both signals miss it.
	go s.refreshURLAfterSwap(newPage)

	log.Printf("[urldriver] swapped tile=%d from %s to %s url=%q",
		s.tileID, oldTargetID, newTargetID, initialURL)
}

// refreshURLAfterSwap polls the new page's TargetInfo URL until it's a
// real http(s) URL (or timeout). Updates lastURL and pushes a nav event.
func (s *Session) refreshURLAfterSwap(page *rod.Page) {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		info, err := page.Info()
		if err != nil {
			return
		}
		if schemeAllowed(info.URL) {
			s.mu.Lock()
			if s.lastURL == info.URL {
				s.mu.Unlock()
				return
			}
			s.lastURL = info.URL
			s.mu.Unlock()
			select {
			case s.navs <- info.URL:
			default:
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (s *Session) frameLoop() {
	defer close(s.framesDone)
	quality := 70
	timer := time.NewTimer(s.streamInterval)
	defer timer.Stop()
	for {
		select {
		case <-s.stopFrames:
			return
		case <-timer.C:
		}
		// Read s.page under lock so a concurrent swap doesn't tear it
		// out from under us. Errors from screenshotting (e.g. when a
		// page is mid-swap) are tolerated; we just skip the frame.
		s.mu.Lock()
		page := s.page
		s.mu.Unlock()
		buf, err := page.Timeout(s.streamInterval+1*time.Second).
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

func (s *Session) pushFrame(buf []byte) {
	for {
		select {
		case s.frames <- buf:
			return
		default:
			select {
			case <-s.frames:
			default:
				return
			}
		}
	}
}

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
		text, unmodText := keyText(ev.Key, mods)
		return proto.InputDispatchKeyEvent{
			Type:                  proto.InputDispatchKeyEventTypeKeyDown,
			Key:                   ev.Key,
			Code:                  ev.Code,
			Modifiers:             mods,
			Text:                  text,
			UnmodifiedText:        unmodText,
			WindowsVirtualKeyCode: virtualKeyCode(ev.Key),
		}.Call(page)
	case InputKeyUp:
		return proto.InputDispatchKeyEvent{
			Type:                  proto.InputDispatchKeyEventTypeKeyUp,
			Key:                   ev.Key,
			Code:                  ev.Code,
			Modifiers:             mods,
			WindowsVirtualKeyCode: virtualKeyCode(ev.Key),
		}.Call(page)
	case InputResize:
		return s.Resize(ev.Width, ev.Height)
	default:
		return fmt.Errorf("unknown input kind %q", ev.Kind)
	}
}

func (s *Session) Resize(w, h int64) error {
	if w <= 0 || h <= 0 {
		return fmt.Errorf("invalid resize %dx%d", w, h)
	}
	s.mu.Lock()
	page := s.page
	s.viewportW = w
	s.viewportH = h
	s.mu.Unlock()
	return page.Timeout(2 * time.Second).SetViewport(&proto.EmulationSetDeviceMetricsOverride{
		Width: int(w), Height: int(h), DeviceScaleFactor: 1,
	})
}

func (s *Session) LastURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastURL
}

func (s *Session) Frames() <-chan []byte    { return s.frames }
func (s *Session) Navs() <-chan string      { return s.navs }
func (s *Session) Done() <-chan struct{}    { return s.done }

func (s *Session) CaptureFinal(_ context.Context) ([]byte, error) {
	quality := 70
	return s.page.Timeout(3 * time.Second).Screenshot(false, &proto.PageCaptureScreenshot{
		Format:  proto.PageCaptureScreenshotFormatJpeg,
		Quality: &quality,
	})
}

func (s *Session) Close() {
	s.closeOnce.Do(func() {
		close(s.stopFrames)
		<-s.framesDone
		s.pageCancel()
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

func keyText(key string, mods int) (text, unmodifiedText string) {
	if utf8.RuneCountInString(key) != 1 {
		return "", ""
	}
	if mods&(2|4) != 0 {
		return "", ""
	}
	return key, strings.ToLower(key)
}

func virtualKeyCode(key string) int {
	if vk, ok := specialKeyVK[key]; ok {
		return vk
	}
	if utf8.RuneCountInString(key) == 1 {
		r := []rune(key)[0]
		switch {
		case r >= 'a' && r <= 'z':
			return int(r - 'a' + 'A')
		case r >= 'A' && r <= 'Z':
			return int(r)
		case r >= '0' && r <= '9':
			return int(r)
		}
	}
	return 0
}

var specialKeyVK = map[string]int{
	"Backspace":   8,
	"Tab":         9,
	"Enter":       13,
	"Shift":       16,
	"Control":     17,
	"Alt":         18,
	"Pause":       19,
	"CapsLock":    20,
	"Escape":      27,
	" ":           32,
	"PageUp":      33,
	"PageDown":    34,
	"End":         35,
	"Home":        36,
	"ArrowLeft":   37,
	"ArrowUp":     38,
	"ArrowRight":  39,
	"ArrowDown":   40,
	"Insert":      45,
	"Delete":      46,
	"Meta":        91,
	"ContextMenu": 93,
	"F1":          112,
	"F2":          113,
	"F3":          114,
	"F4":          115,
	"F5":          116,
	"F6":          117,
	"F7":          118,
	"F8":          119,
	"F9":          120,
	"F10":         121,
	"F11":         122,
	"F12":         123,
}
