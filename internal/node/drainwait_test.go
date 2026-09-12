package node

// The drain bound, from both sides. Close cancels every request context and
// then waits closeDrainWait for the doors to empty: a handler that honors the
// cancellation costs the shutdown nothing, and one that does not costs it the
// bound exactly once.

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// setCloseDrainWait rewrites the drain bound for one test and restores it.
func setCloseDrainWait(t *testing.T, d time.Duration) time.Duration {
	t.Helper()
	was := closeDrainWait
	t.Cleanup(func() { closeDrainWait = was })
	closeDrainWait = d
	return closeDrainWait
}

// nodeServing is a node whose web door runs handle, on a real listener, with
// the production wiring of request contexts: one cancellable base context that
// Close cancels. It returns once a request is inside the handler.
func nodeServing(t *testing.T, handle func(ctx context.Context)) *Node {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	in := make(chan struct{})
	var once bool
	requestCtx, cancel := context.WithCancel(context.Background())
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !once {
			once = true
			close(in)
		}
		handle(r.Context())
	})}
	srv.BaseContext = func(net.Listener) context.Context { return requestCtx }
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	n := &Node{Reg: plugin.NewRegistry(), Ln: ln, st: st, webSrv: srv, cancelRequest: cancel}
	go func() {
		res, err := http.Get("http://" + ln.Addr().String() + "/")
		if err == nil {
			res.Body.Close()
		}
	}()
	select {
	case <-in:
	case <-time.After(10 * time.Second):
		t.Fatal("the request never reached the handler")
	}
	return n
}

// A request that ignores its own cancellation — a blocked syscall, a wedged
// disk — must not hold the shutdown open past the bound.
func TestCloseGivesUpOnADrainAtItsBound(t *testing.T) {
	wait := setCloseDrainWait(t, 200*time.Millisecond)
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	n := nodeServing(t, func(context.Context) { <-stuck })

	type outcome struct {
		took time.Duration
		err  error
	}
	done := make(chan outcome, 1)
	start := time.Now()
	go func() {
		err := n.Close()
		done <- outcome{time.Since(start), err}
	}()
	var got outcome
	select {
	case got = <-done:
	case <-time.After(3 * wait / 2):
		t.Fatalf("Close was still draining after %v — the drain must be bounded by closeDrainWait (%v)", 3*wait/2, wait)
	}
	if got.took < wait {
		t.Fatalf("Close returned after %v, before closeDrainWait (%v) — an in-flight request gets the whole drain", got.took, wait)
	}
	if !errors.Is(got.err, context.DeadlineExceeded) {
		t.Fatalf("Close = %v, want the drain's own deadline — an abandoned request is not a silent shutdown", got.err)
	}
}

// The other side of the same bound: the cancellation Close sends first is what
// normally ends a request, so a handler that honors it costs the shutdown none
// of the wait.
func TestCloseReturnsAsSoonAsTheDoorsEmpty(t *testing.T) {
	// Generous, because what is asserted is that none of it is spent: the
	// drain's own poll for an emptied door is the only delay here.
	wait := setCloseDrainWait(t, 2*time.Second)
	n := nodeServing(t, func(ctx context.Context) { <-ctx.Done() })

	start := time.Now()
	if err := n.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if took := time.Since(start); took >= wait {
		t.Fatalf("Close took %v; a request that ends on the cancellation must not wait out closeDrainWait (%v)", took, wait)
	}
}
