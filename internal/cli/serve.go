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
	"path/filepath"
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
	addr := fs.String("addr", ":8080", "HTTP listen address")
	staticDir := fs.String("static", "./web", "directory of static files served at /")
	insecure := fs.Bool("insecure", false, "do not set the Secure flag on the session cookie (use only when serving over plain HTTP locally)")
	chromiumPath := fs.String("chromium", "", "path to the Chromium binary (empty: auto-detect on PATH; if not found, URL tiles cannot be woken)")
	profileRoot := fs.String("profiles", "", "root directory for per-user Chromium profiles (default: <db dir>/chromium)")
	args = reorderFlagsFirst(args, func(name string) bool {
		return name == "db" || name == "addr" || name == "static" || name == "chromium" || name == "profiles"
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

	root := *profileRoot
	if root == "" {
		root = filepath.Join(filepath.Dir(*db), "chromium")
	}
	driver := urldriver.New(s, urldriver.Config{
		BinaryPath:  *chromiumPath,
		ProfileRoot: root,
	})
	s.SetURLDriver(driver)
	defer driver.Shutdown()
	if driver.Available() {
		fmt.Printf("gridwell: chromium driver ready (profiles=%s)\n", root)
	} else {
		fmt.Println("gridwell: chromium not found — URL tiles cannot be woken; existing previews still render")
	}

	srv := server.New(s, server.Config{
		StaticDir:    *staticDir,
		SecureCookie: !*insecure,
	})
	srv.SetURLStreamer(server.StreamerFromDriver(driver))

	// requestCtx is shared by every incoming request via BaseContext. We
	// cancel it on shutdown so long-running handlers (notably the SSE
	// Subscribe stream) see their context fire Done() immediately, instead
	// of blocking Shutdown until its 10s deadline expires.
	requestCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return requestCtx },
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		fmt.Printf("gridwell: serving on %s (db=%s static=%s)\n", *addr, *db, *staticDir)
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
	// Cancel in-flight request contexts first; SSE handlers exit
	// immediately, allowing Shutdown to drain quickly.
	cancelRequests()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "shutdown: %v\n", err)
		return 1
	}
	return 0
}
