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
	"syscall"
	"time"

	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/store"
	"github.com/josephburnett/gridwell/internal/tmux"
)

// serveFlags holds the parsed `serve` subcommand options. Split out from
// RunServe so the flag-parsing path is unit-testable.
type serveFlags struct {
	DB        string
	Bind      string
	StaticDir string
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
	args = reorderFlagsFirst(args, func(name string) bool {
		switch name {
		case "db", "bind", "static":
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

// RunServe starts the backend HTTP server — the loopback data plane for the
// Gridwell desktop app: Connect-RPC, the SSE event stream, the wasm client,
// and shell PTYs. Live URL tiles are hosted natively by the Electron shell,
// so there is no browser driver here. SIGINT/SIGTERM trigger graceful
// shutdown.
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

	// The gridwell-private tmux server backs every shell tile. One
	// socket per gridwell process; sessions named `gridwell-<tileID>`
	// survive ascents and gridwell restarts (bash + scrollback live
	// in tmux). Reboots take everything with them; the snapshot
	// remains and the wasm hides the refresh button.
	tmuxCtrl, tmuxCleanup, err := tmux.New("gridwell", "")
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: tmux init: %v\n", err)
		return 1
	}
	defer func() { _ = tmuxCleanup() }()

	srv := server.New(s, server.Config{StaticDir: f.StaticDir})
	srv.SetShellStreamer(server.NewLiveShellStreamer(tmuxCtrl))

	// Bound the orphan leak: any tmux session whose tile id no longer
	// exists is left over from a delete that raced a previous crash.
	if killed, err := srv.CleanupOrphanedShellSessions(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "gridwell: orphan cleanup: %v\n", err)
	} else if killed > 0 {
		fmt.Printf("gridwell: orphan cleanup killed %d stale shell session(s)\n", killed)
	}

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
