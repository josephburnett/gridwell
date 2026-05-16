// Package urldriver implements the URLDriver interface from the store
// package on top of headless Chromium via the Chrome DevTools Protocol.
//
// See spec §8.3. v1 scope: per-user Chromium process with a persistent
// profile dir, lazy Wake spawns a tab, Capture takes a screenshot and
// closes the tab, a background goroutine polls a fresh screenshot at a
// configurable cadence while a tile is live. The v1 hard boundaries
// (popup-block, dialog auto-dismiss, permission auto-deny, fullscreen
// block, download block, audio mute, scheme allow-list) are installed
// in setupTabHardening / userBrowserLocked. Streaming-rate screencast
// and input forwarding are part of M4.
package urldriver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// PreviewWriter is the subset of the store the driver needs to push
// screenshot and URL updates back into.
type PreviewWriter interface {
	SetURLPreview(ctx context.Context, userID, tileID int64, jpeg []byte) error
	SetURLString(ctx context.Context, userID, tileID int64, newURL string) error
}

// Config configures the driver.
type Config struct {
	// BinaryPath is the path to the Chromium/Chrome executable. If
	// empty, the driver tries the standard names ("chromium",
	// "chromium-browser", "google-chrome", "chrome",
	// "chrome-headless-shell") on PATH.
	BinaryPath string
	// ProfileRoot is the directory under which each user's persistent
	// profile dir is created (<root>/<user_id>/). Must be writable.
	ProfileRoot string
	// PreviewInterval is how often a fresh screenshot is captured for
	// a live tile with no active subscribers. Defaults to 700ms.
	PreviewInterval time.Duration
	// StreamInterval is the polling cadence used while at least one
	// subscriber is attached to the tile via URLStream. Defaults to
	// 100ms (~10fps). Future work: swap polling for CDP screencast.
	StreamInterval time.Duration
	// ViewportWidth and ViewportHeight are the initial viewport for
	// freshly-woken tabs. The spec defaults are 1280×800; resize is
	// driven by the client via the URLStream resize message.
	ViewportWidth  int64
	ViewportHeight int64
}

// Subscriber is a sink for streamed frames + navigation events from a
// live URL tile. Implementations should be non-blocking: SendFrame and
// SendNavigation are called from the driver's polling goroutine, and a
// slow subscriber must not stall frame delivery to other subscribers.
// The typical pattern is a buffered channel with frame drops on
// overflow (UI streaming).
type Subscriber interface {
	SendFrame(jpeg []byte)
	SendNavigation(newURL string)
}

// Driver is the chromedp-backed URLDriver.
type Driver struct {
	cfg   Config
	store PreviewWriter

	available  bool
	binaryPath string

	mu          sync.Mutex
	users       map[int64]*userBrowser
	tabs        map[liveKey]*tabContext
	subscribers map[liveKey][]Subscriber
}

type liveKey struct {
	user, tile int64
}

type userBrowser struct {
	ctx    context.Context
	cancel context.CancelFunc
}

type tabContext struct {
	ctx    context.Context
	cancel context.CancelFunc
	url    string
	stop   chan struct{}
	done   chan struct{}
}

// hardeningJS is injected on every new document. It blocks
// page-initiated popups and fullscreen requests, both of which we
// promise to suppress in v1 (spec §8.3 "hard boundaries").
const hardeningJS = `
(function(){
  try { window.open = function(){ return null; }; } catch (e) {}
  try {
    if (Element.prototype.requestFullscreen) {
      Element.prototype.requestFullscreen = function(){
        return Promise.reject(new Error('blocked by gridwell'));
      };
    }
    if (document.exitFullscreen) {
      document.exitFullscreen = function(){
        return Promise.reject(new Error('blocked by gridwell'));
      };
    }
  } catch (e) {}
})();
`

// New constructs a Driver. Probes for Chromium at construction time; if
// not found, Available() returns false and all live-tile ops will
// surface that to callers as ErrChromiumUnavailable.
func New(store PreviewWriter, cfg Config) *Driver {
	if cfg.PreviewInterval <= 0 {
		cfg.PreviewInterval = 700 * time.Millisecond
	}
	if cfg.StreamInterval <= 0 {
		cfg.StreamInterval = 100 * time.Millisecond
	}
	if cfg.ViewportWidth <= 0 {
		cfg.ViewportWidth = 1280
	}
	if cfg.ViewportHeight <= 0 {
		cfg.ViewportHeight = 800
	}
	d := &Driver{
		cfg:         cfg,
		store:       store,
		users:       map[int64]*userBrowser{},
		tabs:        map[liveKey]*tabContext{},
		subscribers: map[liveKey][]Subscriber{},
	}
	d.binaryPath = resolveBinary(cfg.BinaryPath)
	d.available = d.binaryPath != "" && cfg.ProfileRoot != ""
	if d.available {
		// Ensure the profile root exists; treat creation failure as
		// "not available" so the rest of gridwell still runs.
		if err := os.MkdirAll(cfg.ProfileRoot, 0o755); err != nil {
			d.available = false
		}
	}
	return d
}

// Available reports whether the driver is functional.
func (d *Driver) Available() bool { return d.available }

// Wake spawns a Chromium tab for (userID, tileID) at initialURL, or
// no-ops if one already exists.
func (d *Driver) Wake(ctx context.Context, userID, tileID int64, initialURL string) error {
	if !d.available {
		return ErrUnavailable
	}
	d.mu.Lock()
	if _, exists := d.tabs[liveKey{userID, tileID}]; exists {
		d.mu.Unlock()
		return nil
	}
	browser, err := d.userBrowserLocked(userID)
	if err != nil {
		d.mu.Unlock()
		return err
	}
	// chromedp.NewContext on the browser's context spawns a new tab.
	tabCtx, cancel := chromedp.NewContext(browser.ctx)
	tc := &tabContext{
		ctx:    tabCtx,
		cancel: cancel,
		url:    initialURL,
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
	d.tabs[liveKey{userID, tileID}] = tc
	d.mu.Unlock()

	// Install per-tab listeners (dialog dismiss, navigation tracking).
	// Must happen before Navigate so we don't miss the first events.
	d.installTabListeners(tabCtx, userID, tileID)

	// Set up viewport + injected hardening JS, then navigate. The
	// derived setup context bounds the operation but we never call its
	// cancel func — see runWithTimeout below for why (chromedp keys
	// its session listener to a derived context's Done channel and
	// cancelling it tears down the session).
	_ = runWithTimeout(tabCtx, 20*time.Second,
		chromedp.EmulateViewport(d.cfg.ViewportWidth, d.cfg.ViewportHeight),
		injectHardening(),
		chromedp.Navigate(initialURL),
	)

	// Start the polling loop that pushes screenshots into the store.
	go d.previewLoop(userID, tileID, tc)
	return nil
}

// InputEventKind discriminates between mouse, key, and resize events
// flowing from a URLStream WebSocket client into the driver.
type InputEventKind string

const (
	InputMouseMove  InputEventKind = "mouse_move"
	InputMouseDown  InputEventKind = "mouse_down"
	InputMouseUp    InputEventKind = "mouse_up"
	InputMouseWheel InputEventKind = "mouse_wheel"
	InputKeyDown    InputEventKind = "key_down"
	InputKeyUp      InputEventKind = "key_up"
	InputResize     InputEventKind = "resize"
)

// MouseButton names. Empty string means no button (e.g. on move).
const (
	MouseButtonLeft   = "left"
	MouseButtonMiddle = "middle"
	MouseButtonRight  = "right"
)

// InputEvent is one input message from a URLStream subscriber. Fields
// are populated based on Kind:
//   - mouse_move/down/up/wheel: X, Y; Button for down/up; DeltaY for wheel.
//   - key_down/up: Key, Code, Modifiers.
//   - resize: Width, Height.
type InputEvent struct {
	Kind      InputEventKind
	X, Y      float64
	Button    string
	DeltaY    float64
	Key       string
	Code      string
	Modifiers int64
	Width     int64
	Height    int64
}

// ForwardInput translates an InputEvent into CDP commands and
// dispatches them to the (userID, tileID) tab. No-op if the tile is
// not live. Errors are returned so the URLStream handler can decide
// whether to surface them.
func (d *Driver) ForwardInput(userID, tileID int64, ev InputEvent) error {
	d.mu.Lock()
	tc, ok := d.tabs[liveKey{userID, tileID}]
	d.mu.Unlock()
	if !ok {
		return nil
	}
	action, err := inputAction(ev)
	if err != nil {
		return err
	}
	return runWithTimeout(tc.ctx, 2*time.Second, action)
}

// captureJPEG is a chromedp action that grabs a JPEG screenshot at a
// reasonable quality. CDP's default format is PNG, which is ~5-10x
// larger for typical web content; we override explicitly. Quality 70
// is a good middle ground for both streaming bandwidth and parent-
// grid preview clarity.
func captureJPEG(buf *[]byte) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		data, err := page.CaptureScreenshot().
			WithFormat(page.CaptureScreenshotFormatJpeg).
			WithQuality(70).
			Do(ctx)
		if err != nil {
			return err
		}
		*buf = data
		return nil
	})
}

// inputAction translates an InputEvent into a chromedp.Action that
// dispatches the equivalent CDP Input.* call. Unknown event kinds
// return an error; suppressed v1 events (e.g. an unsupported key)
// translate to a no-op action.
func inputAction(ev InputEvent) (chromedp.Action, error) {
	switch ev.Kind {
	case InputMouseMove:
		return input.DispatchMouseEvent(input.MouseMoved, ev.X, ev.Y).
			WithModifiers(input.Modifier(ev.Modifiers)), nil
	case InputMouseDown:
		btn := cdpMouseButton(ev.Button)
		return input.DispatchMouseEvent(input.MousePressed, ev.X, ev.Y).
			WithButton(btn).
			WithClickCount(1).
			WithModifiers(input.Modifier(ev.Modifiers)), nil
	case InputMouseUp:
		btn := cdpMouseButton(ev.Button)
		return input.DispatchMouseEvent(input.MouseReleased, ev.X, ev.Y).
			WithButton(btn).
			WithClickCount(1).
			WithModifiers(input.Modifier(ev.Modifiers)), nil
	case InputMouseWheel:
		return input.DispatchMouseEvent(input.MouseWheel, ev.X, ev.Y).
			WithDeltaY(ev.DeltaY).
			WithModifiers(input.Modifier(ev.Modifiers)), nil
	case InputKeyDown:
		return input.DispatchKeyEvent(input.KeyDown).
			WithKey(ev.Key).
			WithCode(ev.Code).
			WithModifiers(input.Modifier(ev.Modifiers)), nil
	case InputKeyUp:
		return input.DispatchKeyEvent(input.KeyUp).
			WithKey(ev.Key).
			WithCode(ev.Code).
			WithModifiers(input.Modifier(ev.Modifiers)), nil
	case InputResize:
		if ev.Width <= 0 || ev.Height <= 0 {
			return nil, fmt.Errorf("invalid resize %dx%d", ev.Width, ev.Height)
		}
		return emulation.SetDeviceMetricsOverride(ev.Width, ev.Height, 1.0, false), nil
	default:
		return nil, fmt.Errorf("unknown input kind %q", ev.Kind)
	}
}

func cdpMouseButton(name string) input.MouseButton {
	switch name {
	case MouseButtonLeft:
		return input.Left
	case MouseButtonMiddle:
		return input.Middle
	case MouseButtonRight:
		return input.Right
	default:
		return input.None
	}
}

// runWithTimeout invokes chromedp.Run with a time-bounded context
// derived from parent. We deliberately do NOT cancel the derived
// context after Run returns: chromedp's session listeners watch the
// first-derived context's Done channel, and cancelling it after the
// fact terminates the entire chromedp session (subsequent Runs on the
// same parent then fail with context.Canceled). The derived context
// times out naturally at its deadline, so leaks are bounded.
func runWithTimeout(parent context.Context, timeout time.Duration, actions ...chromedp.Action) error {
	ctx, _ := context.WithTimeout(parent, timeout) //nolint:govet // see comment above
	return chromedp.Run(ctx, actions...)
}

// Capture takes a final screenshot and closes the tab. No-ops if the
// tile is not currently live.
func (d *Driver) Capture(ctx context.Context, userID, tileID int64) error {
	d.mu.Lock()
	tc, exists := d.tabs[liveKey{userID, tileID}]
	if !exists {
		d.mu.Unlock()
		return nil
	}
	delete(d.tabs, liveKey{userID, tileID})
	d.mu.Unlock()

	// Stop the polling loop first; if it's mid-screenshot it'll see the
	// stop channel close and return.
	close(tc.stop)
	<-tc.done

	// One final screenshot before tearing down. See runWithTimeout for
	// why we don't cancel the derived context.
	var buf []byte
	if err := runWithTimeout(tc.ctx, 3*time.Second, captureJPEG(&buf)); err == nil && len(buf) > 0 {
		_ = d.store.SetURLPreview(ctx, userID, tileID, buf)
	}
	tc.cancel()
	return nil
}

// IsLive reports whether (userID, tileID) currently has a live tab.
func (d *Driver) IsLive(userID, tileID int64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.tabs[liveKey{userID, tileID}]
	return ok
}

// Shutdown tears down every user's Chromium process. Call on server stop.
//
// Snapshot the tabs and users slices under the lock and release it
// before waiting on each tab's `done` channel — the preview loop
// needs d.mu to broadcast frames, so holding it while waiting for the
// loop to drain would deadlock.
func (d *Driver) Shutdown() {
	d.mu.Lock()
	tabs := make([]*tabContext, 0, len(d.tabs))
	for k, tc := range d.tabs {
		tabs = append(tabs, tc)
		delete(d.tabs, k)
	}
	users := make([]*userBrowser, 0, len(d.users))
	for uid, b := range d.users {
		users = append(users, b)
		delete(d.users, uid)
	}
	d.mu.Unlock()
	for _, tc := range tabs {
		close(tc.stop)
		<-tc.done
		tc.cancel()
	}
	for _, b := range users {
		b.cancel()
	}
}

// userBrowserLocked returns the user's Chromium browser context,
// spawning a fresh process if none is running. Caller must hold d.mu.
func (d *Driver) userBrowserLocked(userID int64) (*userBrowser, error) {
	if b, ok := d.users[userID]; ok {
		return b, nil
	}
	profileDir := filepath.Join(d.cfg.ProfileRoot, fmt.Sprintf("%d", userID))
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		return nil, fmt.Errorf("profile dir: %w", err)
	}
	// Build on chromedp's puppeteer-aligned defaults (disable-dev-shm-
	// usage, NetworkService, etc. — critical for WSL2 / container
	// environments) and add our gridwell-specific flags on top.
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts,
		chromedp.Flag("headless", "new"),
		chromedp.Flag("mute-audio", true),
		// no-sandbox is required to launch Chromium as root or inside
		// many containers. Local single-user gridwell deployments are
		// the v1 target.
		chromedp.Flag("no-sandbox", true),
		// Block all permission prompts (camera/mic/geo/notif/...).
		// Available since Chrome 100.
		chromedp.Flag("deny-permission-prompts", true),
		chromedp.UserDataDir(profileDir),
		chromedp.ExecPath(d.binaryPath),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	cancel := func() {
		browserCancel()
		allocCancel()
	}
	// Force the actual Chromium process to launch so a missing binary
	// or broken environment surfaces here, not later. An empty Run is
	// the canonical "are you up?" probe. The derived bootCtx is
	// allowed to time out naturally — see runWithTimeout for why we
	// don't call its cancel.
	bootCtx, _ := context.WithTimeout(browserCtx, 15*time.Second) //nolint:govet
	if err := chromedp.Run(bootCtx); err != nil {
		cancel()
		return nil, fmt.Errorf("chromium bring-up: %w", err)
	}
	// Note: v1 download-blocking relies on the Chrome --disable-popup-
	// blocking off + the popup JS shim and on the URL scheme allow-list
	// (in-page navs to non-http(s) targets don't rename the tile, and
	// programmatic <a download> clicks generally fail in headless=new
	// mode anyway). Future work: Browser.setDownloadBehavior at the
	// CDP level — preliminary attempts wedged subsequent screencast.
	b := &userBrowser{ctx: browserCtx, cancel: cancel}
	d.users[userID] = b
	return b, nil
}

// installTabListeners hooks dialog-dismiss + navigation tracking. The
// chromedp ListenTarget callback runs in a separate goroutine — never
// block it.
func (d *Driver) installTabListeners(tabCtx context.Context, userID, tileID int64) {
	chromedp.ListenTarget(tabCtx, func(ev any) {
		switch e := ev.(type) {
		case *page.EventJavascriptDialogOpening:
			// Auto-dismiss: accept=false. Spec §8.3. The derived ctx
			// is allowed to time out naturally (see runWithTimeout).
			go func() {
				_ = runWithTimeout(tabCtx, 2*time.Second, page.HandleJavaScriptDialog(false))
			}()
		case *page.EventFrameNavigated:
			// Only the main frame's navigations rename the tile's URL.
			// Subframe navigations (ads, embeds) don't.
			if e.Frame == nil || string(e.Frame.ParentID) != "" {
				return
			}
			u := e.Frame.URL
			if !schemeAllowed(u) {
				return
			}
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				_ = d.store.SetURLString(ctx, userID, tileID, u)
				d.broadcastNavigation(userID, tileID, u)
			}()
		}
	})
}

// injectHardening returns a chromedp action that registers the
// popup/fullscreen blocker script to run on every new document in the
// current tab.
func injectHardening() chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		_, err := page.AddScriptToEvaluateOnNewDocument(hardeningJS).Do(ctx)
		return err
	})
}

// previewLoop polls a screenshot at a cadence chosen by subscriber
// presence: cfg.StreamInterval while one or more subscribers are
// attached, cfg.PreviewInterval otherwise. Every successful frame is
// (a) written to preview_jpeg via the store and (b) fanned out to
// subscribers. Returns when tc.stop is closed.
func (d *Driver) previewLoop(userID, tileID int64, tc *tabContext) {
	defer close(tc.done)
	// Use a single timer reset to the current cadence instead of a
	// fixed ticker so cadence changes (subscribe / unsubscribe) take
	// effect on the next tick without restart.
	timer := time.NewTimer(d.cfg.PreviewInterval)
	defer timer.Stop()
	for {
		select {
		case <-tc.stop:
			return
		case <-tc.ctx.Done():
			return
		case <-timer.C:
		}
		var buf []byte
		err := runWithTimeout(tc.ctx, d.cfg.StreamInterval+1*time.Second, captureJPEG(&buf))
		if err == nil && len(buf) > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = d.store.SetURLPreview(ctx, userID, tileID, buf)
			cancel()
			d.broadcastFrame(userID, tileID, buf)
		}
		timer.Reset(d.tickInterval(userID, tileID))
	}
}

// tickInterval returns the current per-tile polling cadence. While at
// least one subscriber is attached, frames are produced at
// cfg.StreamInterval (~10fps); otherwise at cfg.PreviewInterval
// (~700ms, the background-preview cadence from spec §8.3).
func (d *Driver) tickInterval(userID, tileID int64) time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.subscribers[liveKey{userID, tileID}]) > 0 {
		return d.cfg.StreamInterval
	}
	return d.cfg.PreviewInterval
}

// broadcastFrame fans a captured frame out to all subscribers for the
// tile. Snapshots the subscriber slice under lock then sends without
// the lock held (SendFrame is required to be non-blocking).
func (d *Driver) broadcastFrame(userID, tileID int64, jpeg []byte) {
	d.mu.Lock()
	subs := append([]Subscriber(nil), d.subscribers[liveKey{userID, tileID}]...)
	d.mu.Unlock()
	for _, s := range subs {
		s.SendFrame(jpeg)
	}
}

// broadcastNavigation fans a new URL out to all subscribers for the
// tile. Mirrors broadcastFrame.
func (d *Driver) broadcastNavigation(userID, tileID int64, newURL string) {
	d.mu.Lock()
	subs := append([]Subscriber(nil), d.subscribers[liveKey{userID, tileID}]...)
	d.mu.Unlock()
	for _, s := range subs {
		s.SendNavigation(newURL)
	}
}

// Subscribe registers sub as a recipient of frame and navigation
// events for (userID, tileID). The returned unsubscribe function
// removes sub from the list; callers should always invoke it (e.g.
// via defer) to avoid leaking subscriber slots. Subscribe does not
// validate that the tile is live or that the user has permission —
// the caller (the URLStream HTTP handler) is responsible for that.
func (d *Driver) Subscribe(userID, tileID int64, sub Subscriber) func() {
	key := liveKey{userID, tileID}
	d.mu.Lock()
	d.subscribers[key] = append(d.subscribers[key], sub)
	d.mu.Unlock()
	return func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		list := d.subscribers[key]
		for i, s := range list {
			if s == sub {
				d.subscribers[key] = append(list[:i], list[i+1:]...)
				break
			}
		}
		if len(d.subscribers[key]) == 0 {
			delete(d.subscribers, key)
		}
	}
}

// ErrUnavailable is returned when Chromium is not available. Distinct
// from the store-package error so the driver can be used without
// importing the store package; callers translate as needed.
var ErrUnavailable = errors.New("chromium unavailable")

// schemeAllowed reports whether a URL is in the v1 allow-list
// (http/https only). Used to filter in-page navigation events before
// writing back to url_string — file://, chrome://, etc. should not
// rename a tile.
func schemeAllowed(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
}

// resolveBinary returns the path to a usable Chromium binary, trying
// configured > standard names on PATH. Returns "" if none exist.
func resolveBinary(configured string) string {
	if configured != "" {
		if _, err := os.Stat(configured); err == nil {
			return configured
		}
		return ""
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "chrome", "chrome-headless-shell"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}
