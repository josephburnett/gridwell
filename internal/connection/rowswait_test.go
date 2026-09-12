package connection

// The framing bound, from both sides: a landed connection whose Handshake
// hangs answers its row with zero framing when rowsHandshakeWait elapses and
// not before, and one that answers inside it carries its viewport.

import (
	"context"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/connection/dial"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// hangingHandshakeClient lands like any far node and then never answers the
// handshake the row's framing comes from: a tunnel that went away between the
// learn and the menu.
type hangingHandshakeClient struct{ landingClient }

func (hangingHandshakeClient) Handshake(ctx context.Context, _ *gridwellv1.HandshakeRequest) (*gridwellv1.HandshakeResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// framedClient answers the handshake with the far home's viewport.
type framedClient struct{ landingClient }

func (framedClient) Handshake(context.Context, *gridwellv1.HandshakeRequest) (*gridwellv1.HandshakeResponse, error) {
	return &gridwellv1.HandshakeResponse{HomeViewCx: 3, HomeViewCy: 4, HomeViewZoom: 2}, nil
}

// shortRowsHandshakeWait shrinks the framing bound for one test and restores it.
func shortRowsHandshakeWait(t *testing.T) time.Duration {
	t.Helper()
	was := rowsHandshakeWait
	t.Cleanup(func() { rowsHandshakeWait = was })
	rowsHandshakeWait = 200 * time.Millisecond
	return rowsHandshakeWait
}

// landedTransport is one connection already landed on rnode1/7, whose client
// the caller chooses.
func landedTransport(t *testing.T, client namespace.Namespace) *Server {
	t.Helper()
	dialer := func(dial.Config) (namespace.Namespace, func(), error) {
		return client, func() {}, nil
	}
	s, err := New(sharedConnDB(t), dialer, "", []config.ConnectionConfig{{Name: "rtb", Addr: "/s"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.ConnectAll(context.Background())
	return s
}

// The + menu asks for the rows on every opening, so a far node that hangs must
// cost its own row its framing at rowsHandshakeWait and nothing else.
func TestRowsAnswerAtTheHandshakeBoundWhenTheFarNodeHangs(t *testing.T) {
	wait := shortRowsHandshakeWait(t)
	s := landedTransport(t, hangingHandshakeClient{landingClient{root: "rnode1/7"}})

	type outcome struct {
		took time.Duration
		rows []Row
	}
	done := make(chan outcome, 1)
	start := time.Now()
	go func() {
		rows := s.Rows(context.Background())
		done <- outcome{time.Since(start), rows}
	}()
	var got outcome
	select {
	case got = <-done:
	case <-time.After(3 * wait / 2):
		t.Fatalf("Rows was still waiting after %v — the far handshake must be bounded by rowsHandshakeWait (%v)", 3*wait/2, wait)
	}
	if got.took < wait {
		t.Fatalf("Rows returned after %v, before rowsHandshakeWait (%v) — a slow far node gets its wait", got.took, wait)
	}
	if len(got.rows) != 1 || got.rows[0].RootGridID != "rtb/rnode1/7" {
		t.Fatalf("rows = %+v, want the landing the row was learned on", got.rows)
	}
	if r := got.rows[0]; r.ViewCx != 0 || r.ViewCy != 0 || r.ViewZoom != 0 {
		t.Fatalf("row framing = %v,%v,%v; a hung handshake contributes zeros", r.ViewCx, r.ViewCy, r.ViewZoom)
	}
}

// The other side of the same bound: a handshake that answers inside it puts the
// far home's viewport on the row, so the wait is a ceiling and never a delay.
func TestRowsCarryTheFramingOfAConnectionThatAnswers(t *testing.T) {
	shortRowsHandshakeWait(t)
	s := landedTransport(t, framedClient{landingClient{root: "rnode1/7"}})

	start := time.Now()
	rows := s.Rows(context.Background())
	if took := time.Since(start); took >= rowsHandshakeWait {
		t.Fatalf("Rows took %v against a far node that answered at once", took)
	}
	if len(rows) != 1 || rows[0].ViewZoom != 2 || rows[0].ViewCx != 3 || rows[0].ViewCy != 4 {
		t.Fatalf("rows = %+v, want the far home's framing", rows)
	}
}
