package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/store"
	"github.com/josephburnett/gridwell/internal/urldriver"
)

// defaultNoXvfb is true on platforms where Xvfb is unsupported. Linux is
// the only target where Gridwell manages Xvfb; on macOS / *BSD the user
// must drive Chromium headless. Auto-defaulting --no-xvfb (and --headless)
// means `gridwell serve` Just Works without flags on those platforms.
func defaultNoXvfb() bool { return runtime.GOOS != "linux" }

// defaultHeadless mirrors defaultNoXvfb: headful Chromium needs a display
// server, which only Xvfb gives us out-of-the-box on Linux. Off Linux,
// headless is the only option.
func defaultHeadless() bool { return runtime.GOOS != "linux" }

// serveFlags holds the parsed `serve` subcommand options. Split out
// from RunServe so the flag-parsing path is unit-testable; the rest of
// RunServe is server lifecycle that needs a real DB / Xvfb / Chromium
// to exercise.
type serveFlags struct {
	DB             string
	Bind           string
	StaticDir      string
	BrowserName    string
	BrowserBin     string
	ProfileDir     string
	XvfbResolution string
	NoXvfb         bool
	Headless       bool
}

// parseServeFlags parses the `serve` flag set. Returns the populated
// struct, or an error if flag parsing fails (so the caller can decide
// the exit code).
func parseServeFlags(args []string) (serveFlags, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	var f serveFlags
	db := resolveDB(fs, "./gridwell.db")
	fs.StringVar(&f.Bind, "bind", "127.0.0.1:8080", "HTTP listen address (default loopback only)")
	fs.StringVar(&f.StaticDir, "static", "./web", "directory of static files served at /")
	fs.StringVar(&f.BrowserName, "browser", "chromium", "browser brand: "+strings.Join(sortedBrands(), ", "))
	fs.StringVar(&f.BrowserBin, "browser-bin", "", "explicit browser binary path (overrides --browser lookup)")
	fs.StringVar(&f.ProfileDir, "profile-dir", "", "explicit user-data-dir (overrides ~/.gridwell/profiles/<browser>)")
	fs.StringVar(&f.XvfbResolution, "xvfb-resolution", "2560x1600", "Xvfb screen resolution WIDTHxHEIGHT (Linux only)")
	fs.BoolVar(&f.NoXvfb, "no-xvfb", defaultNoXvfb(), "do not spawn Xvfb; inherit DISPLAY from environment (default true on non-Linux)")
	fs.BoolVar(&f.Headless, "headless", defaultHeadless(), "launch Chromium in headless=new mode (default true on non-Linux; required when --no-xvfb on a host with no DISPLAY)")
	args = reorderFlagsFirst(args, func(name string) bool {
		switch name {
		case "db", "bind", "static", "browser", "browser-bin", "profile-dir", "xvfb-resolution":
			return true
		}
		return false
	})
	if err := fs.Parse(args); err != nil {
		return serveFlags{}, err
	}
	f.DB = *db
	return f, nil
}

// RunServe starts the HTTP server. SIGINT/SIGTERM trigger graceful shutdown.
func RunServe(args []string) int {
	f, err := parseServeFlags(args)
	if err != nil {
		return 2
	}

	s, err := store.Open(f.DB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: open db: %v\n", err)
		return 1
	}
	defer s.Close()

	display := ""
	var xv *urldriver.Xvfb
	if !f.NoXvfb {
		w, h, err := parseResolution(f.XvfbResolution)
		if err != nil {
			fmt.Fprintf(os.Stderr, "serve: --xvfb-resolution: %v\n", err)
			return 2
		}
		xv, err = urldriver.StartXvfb(w, h)
		if err != nil {
			fmt.Fprintf(os.Stderr, "serve: xvfb: %v\n", err)
			return 1
		}
		defer xv.Stop()
		display = xv.Display()
		fmt.Printf("gridwell: Xvfb ready on %s (%dx%d)\n", display, w, h)
	}

	driver, err := urldriver.New(s, urldriver.Config{
		Browser:         f.BrowserName,
		BinaryOverride:  f.BrowserBin,
		ProfileOverride: f.ProfileDir,
		Display:         display,
		Headless:        f.Headless,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}
	s.SetURLDriver(driver)
	defer driver.Shutdown()
	profilePath, _ := urldriver.DefaultProfileDir(f.BrowserName)
	if f.ProfileDir != "" {
		profilePath = f.ProfileDir
	}
	fmt.Printf("gridwell: %s driver ready (profile=%s headless=%v)\n",
		f.BrowserName, profilePath, f.Headless)

	srv := server.New(s, server.Config{StaticDir: f.StaticDir})
	srv.SetURLStreamer(server.StreamerFromDriver(driver))
	srv.SetShellStreamer(server.ShellStreamerFromDriver())

	requestCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()

	httpSrv := &http.Server{
		Addr:              f.Bind,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return requestCtx },
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		fmt.Printf("gridwell: serving on %s (db=%s static=%s)\n", f.Bind, f.DB, f.StaticDir)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-stop:
		fmt.Println("gridwell: shutting down")
	case err := <-errCh:
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}
	cancelRequests()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "shutdown: %v\n", err)
		return 1
	}
	return 0
}

func sortedBrands() []string {
	out := urldriver.BrandNames()
	sort.Strings(out)
	return out
}

func parseResolution(s string) (int, int, error) {
	parts := strings.SplitN(s, "x", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected WIDTHxHEIGHT, got %q", s)
	}
	w, err := strconv.Atoi(parts[0])
	if err != nil || w <= 0 {
		return 0, 0, fmt.Errorf("bad width %q", parts[0])
	}
	h, err := strconv.Atoi(parts[1])
	if err != nil || h <= 0 {
		return 0, 0, fmt.Errorf("bad height %q", parts[1])
	}
	return w, h, nil
}
