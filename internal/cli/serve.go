package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/josephburnett/ascent/internal/server"
	"github.com/josephburnett/ascent/internal/store"
)

// RunServe starts the HTTP server. SIGINT/SIGTERM trigger graceful shutdown.
func RunServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	db := resolveDB(fs, "./ascent.db")
	addr := fs.String("addr", ":8080", "HTTP listen address")
	staticDir := fs.String("static", "./web", "directory of static files served at /")
	insecure := fs.Bool("insecure", false, "do not set the Secure flag on the session cookie (use only when serving over plain HTTP locally)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	s, err := store.Open(*db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: open db: %v\n", err)
		return 1
	}
	defer s.Close()

	srv := server.New(s, server.Config{
		StaticDir:    *staticDir,
		SecureCookie: !*insecure,
	})

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	errCh := make(chan error, 1)
	go func() {
		fmt.Printf("ascent: serving on %s (db=%s static=%s)\n", *addr, *db, *staticDir)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-stop:
		fmt.Println("ascent: shutting down")
	case err := <-errCh:
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "shutdown: %v\n", err)
		return 1
	}
	return 0
}
