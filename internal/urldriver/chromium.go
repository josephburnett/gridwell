// Package urldriver implements URL-tile presence on top of headless
// Chromium via the Chrome DevTools Protocol, using github.com/go-rod/rod.
//
// Public surface:
//
//   - Driver: one per gridwell process. Owns the per-user Chromium
//     subprocess (via rod.Launcher) and the rod.Browser connected to it.
//     Exposes Available() and OpenSession.
//   - Session: one Chromium tab whose lifetime matches a single
//     /rpc/URLStream WebSocket. Backed by a *rod.Page. Defined in
//     session.go.
//
// The v1 hard boundaries (popup-block, dialog auto-dismiss, permission
// auto-deny, fullscreen block, window.close neutralization, audio mute,
// scheme allow-list) live in hardeningJS and userBrowserLocked.
//
// rod was chosen over chromedp specifically because rod's *Page object
// survives cross-process renderer swaps (CDP Target.AttachToTarget with
// Flatten:true). chromedp's per-target context model cancels on
// Target.targetDestroyed, which manifests as "context canceled" on every
// cross-origin link click. See the migration write-up in the project
// history for the full reasoning.
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

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
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
	// StreamInterval is the screenshot polling cadence used by an
	// active Session. Defaults to 100ms (~10fps).
	StreamInterval time.Duration
	// ViewportWidth/Height are the initial viewport for a fresh
	// Session that opens without a viewport hint from the client.
	// Defaults: 1280×800. The client immediately resizes to its
	// real pane size in the redesigned protocol.
	ViewportWidth  int64
	ViewportHeight int64
}

// Driver owns one Chromium subprocess and one rod.Browser per user.
// Sessions are issued via OpenSession; they manage their own lifecycle.
type Driver struct {
	cfg   Config
	store PreviewWriter

	available  bool
	binaryPath string

	mu    sync.Mutex
	users map[int64]*userBrowser
}

type userBrowser struct {
	browser  *rod.Browser
	launcher *launcher.Launcher
	cancel   context.CancelFunc // cancels the browser's ctx → watchdog wakes up
}

// hardeningJS is injected on every new document. It blocks
// page-initiated popups and fullscreen requests and neutralizes
// window.close so a page can't shut its own tab (and, when it's the
// last tab, the whole Chromium process).
//
// rod re-installs scripts registered via EvalOnNewDocument when the
// underlying target swaps (cross-process navigation), so this protects
// every renderer the user ever lands on within one Session.
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
// not found, Available() returns false and OpenSession returns
// ErrUnavailable.
func New(store PreviewWriter, cfg Config) *Driver {
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
		if err := os.MkdirAll(cfg.ProfileRoot, 0o755); err != nil {
			d.available = false
		}
	}
	return d
}

// Available reports whether the driver is functional.
func (d *Driver) Available() bool { return d.available }

// Shutdown tears down every user's Chromium process. Active Sessions
// are responsible for their own teardown via Close; closing the
// browser also cascades to any still-open page.
func (d *Driver) Shutdown() {
	d.mu.Lock()
	browsers := make([]*userBrowser, 0, len(d.users))
	for uid, b := range d.users {
		browsers = append(browsers, b)
		delete(d.users, uid)
	}
	d.mu.Unlock()
	for _, b := range browsers {
		// Cancel first so any goroutine watching the browser ctx
		// wakes; then close gracefully; then kill the launcher as a
		// belt-and-braces measure.
		b.cancel()
		_ = b.browser.Close()
		b.launcher.Kill()
	}
}

// userBrowserLocked returns the user's rod.Browser, spawning a Chromium
// process if none is running. Caller must hold d.mu.
func (d *Driver) userBrowserLocked(userID int64) (*userBrowser, error) {
	if b, ok := d.users[userID]; ok {
		return b, nil
	}
	profileDir := filepath.Join(d.cfg.ProfileRoot, fmt.Sprintf("%d", userID))
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		return nil, fmt.Errorf("profile dir: %w", err)
	}

	l := launcher.New().
		Bin(d.binaryPath).
		UserDataDir(profileDir).
		HeadlessNew(true).
		Set("mute-audio").
		// NoSandbox(true) also strips RendererCodeIntegrity which
		// otherwise trips up sandboxless mode on Linux. Plain
		// Set("no-sandbox") does NOT strip that feature.
		NoSandbox(true).
		Set("deny-permission-prompts").
		// Verbose Chromium logging so chromium.log tells us why it
		// exited when it does.
		Set("enable-logging", "stderr").
		Set("v", "1")

	if f, err := os.OpenFile(
		filepath.Join(profileDir, "chromium.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644,
	); err == nil {
		l = l.Logger(f)
	}

	controlURL, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("launch chromium: %w", err)
	}

	browserCtx, cancel := context.WithCancel(context.Background())
	browser := rod.New().Context(browserCtx).ControlURL(controlURL)
	if err := browser.Connect(); err != nil {
		cancel()
		l.Kill()
		return nil, fmt.Errorf("connect chromium: %w", err)
	}

	// Browser watchdog: fires when the connection's context is
	// canceled (Shutdown, or rod tearing it down on connection loss).
	// A cascading browser death means every Session derived from it
	// will also see its page ctx canceled.
	go func() {
		<-browserCtx.Done()
		log.Printf("[urldriver] BROWSER ctx done uid=%d err=%v", userID, browserCtx.Err())
	}()

	b := &userBrowser{browser: browser, launcher: l, cancel: cancel}
	d.users[userID] = b
	return b, nil
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

// InputEvent is one input message routed into Session.Input.
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

// ErrUnavailable is returned when Chromium is not available.
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
