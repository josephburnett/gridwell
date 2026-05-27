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
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/store"
	"github.com/josephburnett/gridwell/internal/urldriver"
)

// RunServe starts the HTTP server. SIGINT/SIGTERM trigger graceful shutdown.
func RunServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	db := resolveDB(fs, "./gridwell.db")
	bind := fs.String("bind", "127.0.0.1:8080", "HTTP listen address (default loopback only)")
	staticDir := fs.String("static", "./web", "directory of static files served at /")
	browserName := fs.String("browser", "chromium", "browser brand: "+strings.Join(sortedBrands(), ", "))
	browserBin := fs.String("browser-bin", "", "explicit browser binary path (overrides --browser lookup)")
	xvfbRes := fs.String("xvfb-resolution", "2560x1600", "Xvfb screen resolution WIDTHxHEIGHT")
	noXvfb := fs.Bool("no-xvfb", false, "do not spawn Xvfb; inherit DISPLAY from environment")
	args = reorderFlagsFirst(args, func(name string) bool {
		switch name {
		case "db", "bind", "static", "browser", "browser-bin", "xvfb-resolution":
			return true
		}
		return false
	})
	if err := fs.Parse(args); err != nil {
		return 2
	}

	s, err := store.Open(*db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: open db: %v\n", err)
		return 1
	}
	defer s.Close()

	display := ""
	var xv *urldriver.Xvfb
	if !*noXvfb {
		w, h, err := parseResolution(*xvfbRes)
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
		Browser:        *browserName,
		BinaryOverride: *browserBin,
		Display:        display,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}
	s.SetURLDriver(driver)
	defer driver.Shutdown()
	fmt.Printf("gridwell: %s driver ready\n", *browserName)

	srv := server.New(s, server.Config{StaticDir: *staticDir})
	srv.SetURLStreamer(server.StreamerFromDriver(driver))

	requestCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()

	httpSrv := &http.Server{
		Addr:              *bind,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return requestCtx },
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		fmt.Printf("gridwell: serving on %s (db=%s static=%s)\n", *bind, *db, *staticDir)
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
