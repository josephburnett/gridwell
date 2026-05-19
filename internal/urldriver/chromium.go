// Package urldriver implements URL-tile presence on top of headless
// Chromium via the Chrome DevTools Protocol.
//
// Public surface:
//
//   - Driver: one per gridwell process. Owns the per-user Chromium
//     subprocess and exposes Available() and OpenSession.
//   - Session: one Chromium tab whose lifetime matches a single
//     /rpc/URLStream WebSocket. Created by OpenSession, torn down by
//     Close. Defined in session.go.
//
// The v1 hard boundaries (popup-block, dialog auto-dismiss, permission
// auto-deny, fullscreen block, window.close neutralization, audio mute,
// scheme allow-list) live in hardeningJS and userBrowserLocked.
package urldriver

import (
	"context"
	"errors"
	"fmt"
	"log"
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

// Driver is the chromedp-backed URLDriver.
// Driver owns one Chromium subprocess per user and hands out per-tab
// Sessions on demand. Active sessions are not tracked centrally —
// each Session manages its own teardown.
type Driver struct {
	cfg   Config
	store PreviewWriter

	available  bool
	binaryPath string

	mu    sync.Mutex
	users map[int64]*userBrowser
}

type userBrowser struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// hardeningJS is injected on every new document. It blocks
// page-initiated popups and fullscreen requests, both of which we
// promise to suppress in v1 (spec §8.3 "hard boundaries"). It also
// neutralizes window.close — without this, a page calling
// window.close() can shut its tab and (when it's the last tab) the
// entire headless Chromium process, killing every URL tile.
const hardeningJS = `
(function(){
  try { window.open = function(){ return null; }; } catch (e) {}
  try { window.close = function(){}; } catch (e) {}
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
		cfg:   cfg,
		store: store,
		users: map[int64]*userBrowser{},
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

// Shutdown tears down every user's Chromium process. Call on server stop.
// Active Sessions are responsible for their own teardown via Close;
// killing the user browser context cascades to any still-open tab.
func (d *Driver) Shutdown() {
	d.mu.Lock()
	users := make([]*userBrowser, 0, len(d.users))
	for uid, b := range d.users {
		users = append(users, b)
		delete(d.users, uid)
	}
	d.mu.Unlock()
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
		// Verbose Chromium logging to stderr so chromium.log captures
		// what the browser was doing right before it exits.
		chromedp.Flag("enable-logging", "stderr"),
		chromedp.Flag("v", "1"),
		chromedp.UserDataDir(profileDir),
		chromedp.ExecPath(d.binaryPath),
	)
	// Pipe Chromium's stdout/stderr into a per-user log file so we can
	// see why it exits when it does.
	if f, err := os.OpenFile(
		filepath.Join(profileDir, "chromium.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644,
	); err == nil {
		opts = append(opts, chromedp.CombinedOutput(f))
	}
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

	// Watchdog: a dead browser context cascades — every chromedp.NewContext
	// derived from it is born canceled. If we see this fire we know to
	// rebuild the browser, not just the tab.
	go func() {
		<-browserCtx.Done()
		log.Printf("[urldriver] BROWSER ctx done uid=%d err=%v", userID, browserCtx.Err())
	}()

	return b, nil
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
