package clientsync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
	"github.com/josephburnett/gridwell/api/gwerr"
)

// Of's transport set is gwerr.IsTransport's, read across the one gRPC-to-Connect
// table: for every code the server can answer, the client says Transport
// exactly when the server side would degrade to a memory. The two spellings
// cannot share a value, since the client sees Connect codes and the node gRPC
// ones, so this is the pin.
func TestOfAgreesWithGwerrIsTransport(t *testing.T) {
	for c := codes.Canceled; c <= codes.Unauthenticated; c++ {
		want := gwerr.IsTransport(status.Error(c, "x"))
		got := Of(connect.NewError(gwerr.ConnectCode(c), errors.New("x"))) == OutcomeTransport
		if got != want {
			t.Errorf("code %v: client Transport=%v, server IsTransport=%v", c, got, want)
		}
	}
}

// TestOf pins the classifier over nil, each coded class, and a bare
// non-connect error, which comes from below the protocol.
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

// TestOfPinsWireCodes crosses the seam Of's transport set depends on: a real
// connect-go client against a dead port, and with a canceled context, is
// Transport. A connect-go upgrade that recodes transport failures fails here
// instead of dropping user data on a blip.
func TestOfPinsWireCodes(t *testing.T) {
	// Nothing listens on port 1.
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

// TestReactTables pins all three policy tables side by side. On Transport no
// table sets DropLocal, since local state reconciles away only on a server
// verdict, and none refetches, since against a flapping link a refetch can
// succeed and revert an optimistic patch whose write never landed.
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
		// ReactSave differs from the other two on Conflict alone: a
		// concurrent edit is about to replace the user's words, so it
		// is surfaced.
		{"ReactSave", ReactSave, map[Outcome]Reaction{
			OutcomeOK:        {},
			OutcomeConflict:  {Refetch: true, Log: true, DropLocal: true},
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

// TestNoDropWithoutVerdict sweeps every table and outcome: DropLocal implies
// the server spoke, and Transport implies no Refetch.
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

func TestIsUnimplemented(t *testing.T) {
	if !IsUnimplemented(connect.NewError(connect.CodeUnimplemented, errors.New("no previews"))) {
		t.Fatal("Unimplemented is a capability miss")
	}
	if IsUnimplemented(connect.NewError(connect.CodeUnavailable, errors.New("x"))) || IsUnimplemented(errors.New("plain")) || IsUnimplemented(nil) {
		t.Fatal("anything else is not")
	}
}

// TestOfReadsOurOwnDeadlineAsTransport pins that inflight.Deadline expiring
// is never a verdict. It classifies by identity, because a transport may
// dress a cancelled request in any code on the way back.
func TestOfReadsOurOwnDeadlineAsTransport(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"bare deadline", context.DeadlineExceeded},
		{"bare cancel", context.Canceled},
		{"wrapped by the transport", fmt.Errorf("Post \"/x\": %w", context.DeadlineExceeded)},
		// A transport that hands the expiry back wearing a coded error
		// outside the transport set.
		{"dressed as a verdict", connect.NewError(connect.CodeUnknown, context.DeadlineExceeded)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Of(c.err); got != OutcomeTransport {
				t.Errorf("Of(%v) = %v, want OutcomeTransport", c.err, got)
			}
		})
	}
}

func TestReactGridRead(t *testing.T) {
	cases := []struct {
		name            string
		asked, answered string
		o               Outcome
		want            GridRead
	}{
		{"answered as asked", "n1/7", "n1/7", OutcomeOK, GridRead{Latch: LatchClear, Store: true}},
		{"answered under another id latches, reports, and still stores", "n1/7", "n1/8", OutcomeOK, GridRead{Latch: LatchSet, Store: true, Renamed: true}},
		{"transport touches no latch", "n1/7", "", OutcomeTransport, GridRead{}},
		{"a verdict latches", "n1/7", "", OutcomeRejected, GridRead{Latch: LatchSet}},
		{"a conflict latches", "n1/7", "", OutcomeConflict, GridRead{Latch: LatchSet}},
	}
	for _, c := range cases {
		if got := ReactGridRead(c.asked, c.answered, c.o); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestNoticesFor(t *testing.T) {
	cases := []struct {
		name     string
		r        Reaction
		o        Outcome
		ownWords bool
		want     Notices
	}{
		{"success says nothing", React(OutcomeOK), OutcomeOK, true, Notices{}},
		{"a verdict with no words gets the generic line", React(OutcomeRejected), OutcomeRejected, false, Notices{Generic: true}},
		{"a verdict with words gets both", React(OutcomeRejected), OutcomeRejected, true, Notices{Generic: true, Own: OwnFailed}},
		{"transport with no words gets the generic line", React(OutcomeTransport), OutcomeTransport, false, Notices{Generic: true}},
		{"transport with words says will-retry alone", React(OutcomeTransport), OutcomeTransport, true, Notices{Own: OwnRetry}},
		{"a conflict is silent generically and spoken in its own words", React(OutcomeConflict), OutcomeConflict, true, Notices{Own: OwnFailed}},
	}
	for _, c := range cases {
		if got := NoticesFor(c.r, c.o, c.ownWords); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestReactRead(t *testing.T) {
	for o, want := range map[Outcome]LatchVerdict{
		OutcomeOK: LatchClear, OutcomeTransport: LatchKeep, OutcomeRejected: LatchSet, OutcomeConflict: LatchSet,
	} {
		if got := ReactRead(o); got != want {
			t.Errorf("outcome %v: got %v, want %v", o, got, want)
		}
	}
}
