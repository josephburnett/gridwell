package connection

// The framing bound, from both sides: a landed connection whose Info
// hangs answers its row with zero framing when rowsInfoWait elapses and
// not before, and one that answers inside it carries its viewport.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/connection/dial"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// hangingInfoClient lands like any far node and then never answers the Info
// the row's framing comes from: a tunnel that went away between the learn and
// the menu.
type hangingInfoClient struct {
	landingClient
	learned *atomic.Bool
}

func (c hangingInfoClient) Info(ctx context.Context, req *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	if c.learned.CompareAndSwap(false, true) {
		return c.landingClient.Info(ctx, req)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

// framedClient answers Info with its home's framing, as a node does.
type framedClient struct{ landingClient }

func (c framedClient) Info(context.Context, *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	return &gridwellv1.InfoResponse{RootGridId: c.root, RootViewCx: 3, RootViewCy: 4, RootViewZoom: 2}, nil
}

// shortRowsInfoWait shrinks the framing bound for one test and restores it.
func shortRowsInfoWait(t *testing.T) time.Duration {
	t.Helper()
	was := rowsInfoWait
	t.Cleanup(func() { rowsInfoWait = was })
	rowsInfoWait = 200 * time.Millisecond
	return rowsInfoWait
}

// landedTransport is one connection already landed on rnode1/7, whose client
// the caller chooses.
func landedTransport(t *testing.T, client namespace.Namespace) *Server {
	t.Helper()
	dialer := func(dial.Config) (namespace.Namespace, func(), error) {
		return client, func() {}, nil
	}
	s, err := New(openStore(t), dialer, "", []config.ConnectionConfig{{Name: "rtb", Addr: "/s"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.ConnectAll(context.Background())
	return s
}

// The + menu asks for the rows on every opening, so a far node that hangs must
// cost its own row its framing at rowsInfoWait and nothing else.
func TestRowsAnswerAtTheInfoBoundWhenTheFarNodeHangs(t *testing.T) {
	wait := shortRowsInfoWait(t)
	s := landedTransport(t, hangingInfoClient{landingClient{root: "rnode1/7"}, new(atomic.Bool)})

	type outcome struct {
		took time.Duration
		rows []*gridwellv1.SourceInfo
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
		t.Fatalf("Rows was still waiting after %v — the far Info must be bounded by rowsInfoWait (%v)", 3*wait/2, wait)
	}
	if got.took < wait {
		t.Fatalf("Rows returned after %v, before rowsInfoWait (%v) — a slow far node gets its wait", got.took, wait)
	}
	if len(got.rows) != 1 || got.rows[0].RootGridId != "rtb/rnode1/7" {
		t.Fatalf("rows = %+v, want the landing the row was learned on", got.rows)
	}
	if r := got.rows[0]; r.RootViewCx != 0 || r.RootViewCy != 0 || r.RootViewZoom != 0 {
		t.Fatalf("row framing = %v,%v,%v; a hung Info contributes zeros", r.RootViewCx, r.RootViewCy, r.RootViewZoom)
	}
}

// The other side of the same bound: an Info that answers inside it puts the
// far home's viewport on the row, so the wait is a ceiling and never a delay.
func TestRowsCarryTheFramingOfAConnectionThatAnswers(t *testing.T) {
	shortRowsInfoWait(t)
	s := landedTransport(t, framedClient{landingClient{root: "rnode1/7"}})

	start := time.Now()
	rows := s.Rows(context.Background())
	if took := time.Since(start); took >= rowsInfoWait {
		t.Fatalf("Rows took %v against a far node that answered at once", took)
	}
	if len(rows) != 1 || rows[0].RootViewZoom != 2 || rows[0].RootViewCx != 3 || rows[0].RootViewCy != 4 {
		t.Fatalf("rows = %+v, want the far home's framing", rows)
	}
}
