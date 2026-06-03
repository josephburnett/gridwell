//go:build !js

// Package urldriver implements URL-tile presence on top of headful Chromium
// (or a Chromium-derivative like Brave, Chrome, Edge) via the Chrome DevTools
// Protocol, using github.com/go-rod/rod.
//
// Single-tenant: one browser process per Driver. The browser uses the user's
// default profile directory for the selected brand, so extensions, cookies,
// and login state persist across runs and match what the user sees when
// running the browser interactively from a shell.
package urldriver

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
)

// PreviewWriter is the subset of the store the driver needs to push
// screenshot and URL updates back into.
type PreviewWriter interface {
	SetURLPreview(ctx context.Context, tileID int64, jpeg []byte) error
	SetURLString(ctx context.Context, tileID int64, newURL string) error
}

// Config configures the driver.
type Config struct {
	// Browser is the brand name from the registry (chromium, chrome,
	// brave, edge). Defaults to "chromium" if empty.
	Browser string
	// BinaryOverride, when non-empty, is used as the browser binary
	// path instead of looking up the brand's standard names on PATH.
	BinaryOverride string
	// ProfileOverride, when non-empty, is the user-data-dir to pass to
	// the browser. When empty, the brand's standard $HOME-relative
	// user-data-dir is used. Tests use this to keep their profile data
	// out of the user's real Chrome/Brave profile.
	ProfileOverride string
	// StreamInterval is the screenshot polling cadence used by an
	// active Session. Defaults to 100ms (~10fps).
	StreamInterval time.Duration
	// ViewportWidth/Height are the initial viewport for a fresh
	// Session that opens without a viewport hint from the client.
	ViewportWidth  int64
	ViewportHeight int64
	// Display is the X11 DISPLAY value to set when launching the browser.
	// Empty means inherit the parent process's DISPLAY.
	Display string
	// Headless, when true, launches Chromium in headless=new mode.
	// Production: false (we want extensions, native messaging, focus
	// semantics). Tests: true so the driver works without a display.
	Headless bool
}

// Driver owns one Chromium-family subprocess. Sessions issued via OpenSession
// manage their own lifecycle.
type Driver struct {
	cfg   Config
	store PreviewWriter

	binaryPath string
	brandFlags []string
	profileDir string

	mu      sync.Mutex
	browser *userBrowser
}

type userBrowser struct {
	browser  *rod.Browser
	launcher *launcher.Launcher
	cancel   context.CancelFunc
}

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

// New constructs a Driver. Returns an error if the requested browser brand
// is unknown, the binary can't be found, or $HOME can't be resolved — never
// silently falls back. Callers must abort on error.
func New(store PreviewWriter, cfg Config) (*Driver, error) {
	if cfg.StreamInterval <= 0 {
		cfg.StreamInterval = 100 * time.Millisecond
	}
	if cfg.ViewportWidth <= 0 {
		cfg.ViewportWidth = 1280
	}
	if cfg.ViewportHeight <= 0 {
		cfg.ViewportHeight = 800
	}
	if cfg.Browser == "" {
		cfg.Browser = "chromium"
	}

	binaryPath, err := ResolveBinary(cfg.Browser, cfg.BinaryOverride)
	if err != nil {
		return nil, err
	}
	profileDir := cfg.ProfileOverride
	if profileDir == "" {
		profileDir, err = DefaultProfileDir(cfg.Browser)
		if err != nil {
			return nil, err
		}
	}

	return &Driver{
		cfg:        cfg,
		store:      store,
		binaryPath: binaryPath,
		profileDir: profileDir,
		brandFlags: BrandExtraFlags(cfg.Browser),
	}, nil
}

// Available reports whether the driver is functional. Always true for any
// Driver returned by New (errors are surfaced at construction time).
func (d *Driver) Available() bool { return d != nil }

// Shutdown tears down the Chromium subprocess.
func (d *Driver) Shutdown() {
	d.mu.Lock()
	b := d.browser
	d.browser = nil
	d.mu.Unlock()
	if b == nil {
		return
	}
	b.cancel()
	_ = b.browser.Close()
	b.launcher.Kill()
}

// ensureBrowserLocked returns the browser, spawning it if it isn't already
// running. Caller must hold d.mu.
func (d *Driver) ensureBrowserLocked() (*userBrowser, error) {
	if d.browser != nil {
		return d.browser, nil
	}

	if err := os.MkdirAll(d.profileDir, 0o755); err != nil {
		return nil, fmt.Errorf("profile dir: %w", err)
	}

	l := launcher.New().
		Bin(d.binaryPath).
		UserDataDir(d.profileDir).
		Set("mute-audio").
		Set("deny-permission-prompts")
	if d.cfg.Headless {
		l = l.HeadlessNew(true)
	} else {
		// Headful: extensions only work reliably in a real browser
		// process. The X server is Xvfb (managed by the gridwell
		// process); see internal/urldriver/xvfb.go.
		l = l.Headless(false)
	}

	for _, f := range d.brandFlags {
		l = l.Set(flags.Flag(f))
	}

	if d.cfg.Display != "" {
		l = l.Env("DISPLAY=" + d.cfg.Display)
	}

	logPath := filepath.Join(d.profileDir, "gridwell-browser.log")
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		l = l.Logger(f)
	}

	controlURL, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("launch browser: %w", err)
	}

	browserCtx, cancel := context.WithCancel(context.Background())
	browser := rod.New().Context(browserCtx).ControlURL(controlURL)
	if err := browser.Connect(); err != nil {
		cancel()
		l.Kill()
		return nil, fmt.Errorf("connect browser: %w", err)
	}

	go func() {
		<-browserCtx.Done()
		log.Printf("[urldriver] BROWSER ctx done err=%v", browserCtx.Err())
	}()

	b := &userBrowser{browser: browser, launcher: l, cancel: cancel}
	d.browser = b
	return b, nil
}

func schemeAllowed(u string) bool {
	return len(u) >= 7 && (u[:7] == "http://" || (len(u) >= 8 && u[:8] == "https://"))
}

