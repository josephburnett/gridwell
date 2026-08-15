package clientsync

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"connectrpc.com/connect"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
)

// TestOf pins the classifier over constructed errors: nil, each coded
// class, and a bare non-connect error (which can only come from below the
// protocol, so it is Transport).
func TestOf(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want Outcome
	}{
		{"nil is ok", nil, OutcomeOK},
		{"failed precondition is conflict", connect.NewError(connect.CodeFailedPrecondition, errors.New("version")), OutcomeConflict},
		{"unavailable is transport", connect.NewError(connect.CodeUnavailable, errors.New("refused")), OutcomeTransport},
		{"deadline is transport", connect.NewError(connect.CodeDeadlineExceeded, errors.New("timeout")), OutcomeTransport},
		{"canceled is transport", connect.NewError(connect.CodeCanceled, errors.New("canceled")), OutcomeTransport},
		{"bare error is transport", errors.New("tcp reset by peer"), OutcomeTransport},
		{"invalid argument is rejected", connect.NewError(connect.CodeInvalidArgument, errors.New("bad")), OutcomeRejected},
		{"not found is rejected", connect.NewError(connect.CodeNotFound, errors.New("gone")), OutcomeRejected},
		{"internal is rejected", connect.NewError(connect.CodeInternal, errors.New("boom")), OutcomeRejected},
		{"unimplemented is rejected", connect.NewError(connect.CodeUnimplemented, errors.New("no previews")), OutcomeRejected},
		{"wrapped connect error unwraps", errors.Join(errors.New("ctx"), connect.NewError(connect.CodeUnavailable, errors.New("refused"))), OutcomeTransport},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Of(c.err); got != c.want {
				t.Errorf("Of(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

// TestOfPinsWireCodes crosses the seam Of's transport set depends on: a
// REAL connect-go client (the same generated stub the wasm client uses)
// against a dead port, and with a canceled context, must classify
// Transport. If a connect-go upgrade ever changes how it codes transport
// failures, this fails loudly instead of silently reopening the
// drop-user-data-on-a-blip class (2026-08-14).
func TestOfPinsWireCodes(t *testing.T) {
	// Connection refused: nothing listens on port 1.
	cl := gridwellv1connect.NewGridwellClient(http.DefaultClient, "http://127.0.0.1:1", connect.WithProtoJSON())
	_, err := cl.GetGrid(context.Background(), connect.NewRequest(&pb.GetGridRequest{GridId: "x"}))
	if got := Of(err); got != OutcomeTransport {
		t.Errorf("dead server: Of(%v) = %v, want OutcomeTransport", err, got)
	}

	// Canceled context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = cl.GetGrid(ctx, connect.NewRequest(&pb.GetGridRequest{GridId: "x"}))
	if got := Of(err); got != OutcomeTransport {
		t.Errorf("canceled: Of(%v) = %v, want OutcomeTransport", err, got)
	}
}

// TestReactTables pins all three policy tables side by side. The single
// load-bearing row is Transport: NO table may set DropLocal there — local
// state (a dirty buffer, a pending framing value) may only reconcile away
// on a server verdict. And no table may Refetch on Transport: against a
// flapping link a refetch can succeed and revert an optimistic patch whose
// write never landed.
func TestReactTables(t *testing.T) {
	tables := []struct {
		name  string
		react func(Outcome) Reaction
		want  map[Outcome]Reaction
	}{
		{"React", React, map[Outcome]Reaction{
			OutcomeOK:        {},
			OutcomeConflict:  {Refetch: true},
			OutcomeRejected:  {Log: true},
			OutcomeTransport: {Log: true},
		}},
		{"ReactOptimistic", ReactOptimistic, map[Outcome]Reaction{
			OutcomeOK:        {},
			OutcomeConflict:  {Refetch: true, DropLocal: true},
			OutcomeRejected:  {Refetch: true, Log: true, DropLocal: true},
			OutcomeTransport: {Log: true, Retry: true},
		}},
		{"ReactSave", ReactSave, map[Outcome]Reaction{
			OutcomeOK:        {},
			OutcomeConflict:  {Refetch: true, DropLocal: true},
			OutcomeRejected:  {Refetch: true, Log: true, DropLocal: true},
			OutcomeTransport: {Log: true, Retry: true},
		}},
	}
	for _, tb := range tables {
		for o, want := range tb.want {
			if got := tb.react(o); got != want {
				t.Errorf("%s(%v) = %+v, want %+v", tb.name, o, got, want)
			}
		}
	}
}

// TestNoDropWithoutVerdict is the class invariant stated as a sweep: for
// every policy table and every outcome, DropLocal ⇒ the server spoke
// (never Transport), and Transport ⇒ no Refetch.
func TestNoDropWithoutVerdict(t *testing.T) {
	for _, react := range []func(Outcome) Reaction{React, ReactOptimistic, ReactSave} {
		r := react(OutcomeTransport)
		if r.DropLocal {
			t.Errorf("a policy table drops local state on Transport: %+v", r)
		}
		if r.Refetch {
			t.Errorf("a policy table refetches on Transport: %+v", r)
		}
	}
}
