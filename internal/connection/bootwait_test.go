package connection

// The boot wait, from both sides: a node whose connection never answers still
// comes up, and one whose connection answers inside the wait comes up with it
// live. Every wait here is derived from bootDialWait, which the tests shorten
// so the property is the real one and the suite is not five seconds slower per
// silent connection.

import (
	"context"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/connection/dial"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// gatedClient is a far node that answers Info only once the gate opens.
type gatedClient struct {
	landingClient
	gate <-chan struct{}
}

func (c gatedClient) Info(ctx context.Context, req *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	select {
	case <-c.gate:
		return c.landingClient.Info(ctx, req)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// shortBootDialWait shrinks the boot wait for one test and restores it.
func shortBootDialWait(t *testing.T) time.Duration {
	t.Helper()
	was := bootDialWait
	t.Cleanup(func() { bootDialWait = was })
	bootDialWait = 100 * time.Millisecond
	return bootDialWait
}

func gatedTransport(t *testing.T, gate <-chan struct{}) *Server {
	t.Helper()
	dialer := func(dial.Config) (namespace.Namespace, func(), error) {
		return gatedClient{landingClient: landingClient{root: "rnode1/7"}, gate: gate}, func() {}, nil
	}
	s, err := New(sharedConnDB(t), dialer, "", []config.ConnectionConfig{{Name: "rtb", Addr: "/s"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// A machine whose connection is unreachable still serves its own home: boot
// waits bootDialWait and goes on, and the dial that outlived it lands the
// connection later with no restart.
func TestBootServesAnywayWhenAConnectionNeverAnswers(t *testing.T) {
	wait := shortBootDialWait(t)
	ctx := context.Background()
	gate := make(chan struct{})
	s := gatedTransport(t, gate)

	start := time.Now()
	returned := make(chan time.Duration, 1)
	go func() {
		s.ConnectAll(ctx)
		returned <- time.Since(start)
	}()
	select {
	case took := <-returned:
		if took < wait {
			t.Fatalf("ConnectAll returned after %v, before bootDialWait (%v) — a connection gets its wait", took, wait)
		}
	case <-time.After(10 * wait):
		t.Fatalf("a connection that never answers held up serve past bootDialWait (%v)", wait)
	}
	if rows := s.Rows(ctx); len(rows) != 1 || rows[0].RootGridId != "" {
		t.Fatalf("rows = %+v, want the connection pending, not landed", rows)
	}

	// The dial ConnectAll walked away from is still trying.
	close(gate)
	deadline := time.Now().Add(30 * wait)
	for {
		if rows := s.Rows(ctx); rows[0].RootGridId == "rtb/rnode1/7" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a connection that answered after boot never landed — the dial must keep trying in the background")
		}
		time.Sleep(wait / 10)
	}
}

// A connection that answers inside the wait is live when serve begins, so the
// first client to ask sees its landing rather than a pending row.
func TestBootWaitsForAConnectionThatAnswersInsideIt(t *testing.T) {
	wait := shortBootDialWait(t)
	ctx := context.Background()
	gate := make(chan struct{})
	s := gatedTransport(t, gate)
	go func() {
		time.Sleep(wait / 4)
		close(gate)
	}()

	s.ConnectAll(ctx)

	rows := s.Rows(ctx)
	if len(rows) != 1 || rows[0].RootGridId != "rtb/rnode1/7" {
		t.Fatalf("rows = %+v, want the connection landed before ConnectAll returned", rows)
	}
}
