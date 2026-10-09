package clientsync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/client/inflight"
)

// Of's transport set is gwerr.IsTransport's, read across the one gRPC-to-Connect
// table: for every code the server can answer, the client says Transport
// exactly when the server side would degrade to a memory. The two spellings
// cannot share a value, since the client sees Connect codes and the node gRPC
// ones, so this is the pin. Abandoned is no wire code: a Canceled the far side
// sent is a hop that never spoke, on both sides; only the client's own
// context.Canceled is a call it withdrew.
func TestOfAgreesWithGwerrIsTransport(t *testing.T) {
	for c := codes.Canceled; c <= codes.Unauthenticated; c++ {
		want := gwerr.IsTransport(status.Error(c, "x"))
		o := Of(connect.NewError(gwerr.ConnectCode(c), errors.New("x")))
		if got := o == OutcomeTransport; got != want {
			t.Errorf("code %v: client Transport=%v, server IsTransport=%v", c, got, want)
		}
		if o == OutcomeAbandoned {
			t.Errorf("code %v from the wire reads as the client's own cancel", c)
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
		{"the door refusing this page's build is transport", gwerr.StaleBuild("nodebuild", "pagebuild"), OutcomeTransport},
		{"unavailable is transport", connect.NewError(connect.CodeUnavailable, errors.New("refused")), OutcomeTransport},
		{"deadline is transport", connect.NewError(connect.CodeDeadlineExceeded, errors.New("timeout")), OutcomeTransport},
		{"canceled from the far side is transport", connect.NewError(connect.CodeCanceled, errors.New("canceled")), OutcomeTransport},
		{"our own cancel is abandoned", context.Canceled, OutcomeAbandoned},
		{"our own cancel through connect is abandoned", connect.NewError(connect.CodeCanceled, context.Canceled), OutcomeAbandoned},
		{"our own cancel wrapped by the transport is abandoned", fmt.Errorf("Post \"/x\": %w", context.Canceled), OutcomeAbandoned},
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
// connect-go client against a dead port is Transport, and with a canceled
// context is Abandoned. A connect-go upgrade that recodes transport failures fails here
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
	if got := Of(err); got != OutcomeAbandoned {
		t.Errorf("canceled: Of(%v) = %v, want OutcomeAbandoned", err, got)
	}
}

// heldGrid answers GetGrid only once its caller gives up.
type heldGrid struct {
	gridwellv1connect.UnimplementedGridwellHandler
	asked chan struct{}
}

func (h heldGrid) GetGrid(ctx context.Context, _ *connect.Request[pb.GetGridRequest]) (*connect.Response[pb.GetGridResponse], error) {
	close(h.asked)
	<-ctx.Done()
	return nil, connect.NewError(connect.CodeCanceled, ctx.Err())
}

// A health transition cancels every read in flight for its source and asks
// again (inflight.Reads.CancelIf). The cancelled read is the client's own
// doing, so across a real connect call it latches nothing, says nothing and
// resolves nothing: the storm of 2026-10-02 was each one posting "grid
// unavailable: context canceled".
func TestACancelledReadLeavesNoTrace(t *testing.T) {
	h := heldGrid{asked: make(chan struct{})}
	mux := http.NewServeMux()
	mux.Handle(gridwellv1connect.NewGridwellHandler(h))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cl := gridwellv1connect.NewGridwellClient(srv.Client(), srv.URL, connect.WithProtoJSON())

	reads := inflight.NewReads()
	const key = "n1/g"
	ctx, done, ok := reads.Ask(key)
	if !ok {
		t.Fatal("first ask refused")
	}
	errc := make(chan error, 1)
	go func() {
		_, err := cl.GetGrid(ctx, connect.NewRequest(&pb.GetGridRequest{GridId: key}))
		errc <- err
	}()
	<-h.asked
	reads.CancelIf(func(string) bool { return true })
	err := <-errc
	done()

	o := Of(err)
	if o != OutcomeAbandoned {
		t.Fatalf("Of(%v) = %v, want OutcomeAbandoned", err, o)
	}
	reads.Settle(key, ReactRead(o))
	if reads.Failed(key) {
		t.Error("a withdrawn read latched its key")
	}
	if ReadSurfaces(o) {
		t.Error("a withdrawn read surfaces")
	}
	if g := ReactGridRead(key, "", o); g.Surface || g.Resolve || g.Store || g.Renamed {
		t.Errorf("ReactGridRead(abandoned) = %+v, want nothing", g)
	}
	if p := ReactPreview(err, true); p.Surface || p.Settle || p.Store {
		t.Errorf("ReactPreview(abandoned) = %+v, want nothing", p)
	}
	if _, _, ok := reads.Ask(key); !ok {
		t.Error("the canceller's re-ask was refused")
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
			OutcomeAbandoned: {Log: true},
		}},
		{"ReactOptimistic", ReactOptimistic, map[Outcome]Reaction{
			OutcomeOK:        {},
			OutcomeConflict:  {Refetch: true, DropLocal: true},
			OutcomeRejected:  {Refetch: true, Log: true, DropLocal: true},
			OutcomeTransport: {Log: true, Retry: true},
			OutcomeAbandoned: {Log: true, Retry: true},
		}},
		// ReactSave differs from the other two on Conflict alone: a
		// concurrent edit is about to replace the user's words, so it
		// is surfaced.
		{"ReactSave", ReactSave, map[Outcome]Reaction{
			OutcomeOK:        {},
			OutcomeConflict:  {Refetch: true, Log: true, DropLocal: true},
			OutcomeRejected:  {Refetch: true, Log: true, DropLocal: true},
			OutcomeTransport: {Log: true, Retry: true},
			OutcomeAbandoned: {Log: true, Retry: true},
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
// the server spoke, and an unheard call implies no Refetch.
func TestNoDropWithoutVerdict(t *testing.T) {
	for _, react := range []func(Outcome) Reaction{React, ReactOptimistic, ReactSave} {
		for _, o := range []Outcome{OutcomeTransport, OutcomeAbandoned} {
			if !Unheard(o) {
				t.Errorf("%v is not unheard", o)
			}
			r := react(o)
			if r.DropLocal {
				t.Errorf("a policy table drops local state on %v: %+v", o, r)
			}
			if r.Refetch {
				t.Errorf("a policy table refetches on %v: %+v", o, r)
			}
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
		{"answered as asked", "n1/7", "n1/7", OutcomeOK, GridRead{Latch: inflight.Answered, Store: true, Resolve: true}},
		{"answered under another id latches, reports, and still stores", "n1/7", "n1/8", OutcomeOK, GridRead{Latch: inflight.Refused, Store: true, Renamed: true}},
		{"transport latches unreachable", "n1/7", "", OutcomeTransport, GridRead{Latch: inflight.Unreachable, Surface: true}},
		{"a verdict latches", "n1/7", "", OutcomeRejected, GridRead{Latch: inflight.Refused, Surface: true}},
		{"a conflict latches", "n1/7", "", OutcomeConflict, GridRead{Latch: inflight.Refused, Surface: true}},
		{"a withdrawn read does nothing", "n1/7", "", OutcomeAbandoned, GridRead{Latch: inflight.Abandoned}},
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
		{"a withdrawn write says will-retry alone", React(OutcomeAbandoned), OutcomeAbandoned, true, Notices{Own: OwnRetry}},
		{"a conflict is silent generically and spoken in its own words", React(OutcomeConflict), OutcomeConflict, true, Notices{Own: OwnFailed, Reloaded: true}},
		{"an optimistic conflict says it reloaded", ReactOptimistic(OutcomeConflict), OutcomeConflict, false, Notices{Reloaded: true}},
		{"an optimistic rejection refetches without claiming a change elsewhere", ReactOptimistic(OutcomeRejected), OutcomeRejected, false, Notices{Generic: true}},
		{"a save conflict says it reloaded", ReactSave(OutcomeConflict), OutcomeConflict, true, Notices{Generic: true, Own: OwnFailed, Reloaded: true}},
		{"a save rejection does not", ReactSave(OutcomeRejected), OutcomeRejected, true, Notices{Generic: true, Own: OwnFailed}},
	}
	for _, c := range cases {
		if got := NoticesFor(c.r, c.o, c.ownWords); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

// A read the renderer re-asks every frame must latch on every failure, or the
// failure's own notice draws the frame that asks again. Only a verdict
// stands until a change; an unreachable source is re-asked by the backstop.
func TestReactRead(t *testing.T) {
	for o, want := range map[Outcome]inflight.Verdict{
		OutcomeOK:        inflight.Answered,
		OutcomeTransport: inflight.Unreachable,
		OutcomeRejected:  inflight.Refused,
		OutcomeConflict:  inflight.Refused,
		OutcomeAbandoned: inflight.Abandoned,
	} {
		if got := ReactRead(o); got != want {
			t.Errorf("outcome %v: got %v, want %v", o, got, want)
		}
	}
	// The two a body read meets: the plugin's not_found for a file gone from
	// disk, and a dark plugin's unavailable.
	gone := connect.NewError(connect.CodeNotFound, errors.New("plugin: no tile ~Zm9v"))
	if got := ReactRead(Of(gone)); got != inflight.Refused {
		t.Errorf("not_found: got %v, want Refused", got)
	}
	dark := connect.NewError(connect.CodeUnavailable, errors.New("plugin down"))
	if got := ReactRead(Of(dark)); got != inflight.Unreachable {
		t.Errorf("unavailable: got %v, want Unreachable", got)
	}
}

// deadAnswer is what the browser receives for a link broken at any hop: the
// node's gwerr.DeadRef through the Connect codec.
func deadAnswer() error {
	dead := gwerr.DeadRef("toc", "connection: no connection %q", "toc")
	return gwerr.ConnectDetails(dead, connect.NewError(connect.CodeNotFound, errors.New(status.Convert(dead).Message())))
}

// A dead link is a state: a read of it latches dead and surfaces nothing. A
// write that meets it is refused like any other verdict, and a plain NotFound
// stays a verdict that surfaces.
func TestTheDeadVerdict(t *testing.T) {
	if got := Of(deadAnswer()); got != OutcomeDead {
		t.Fatalf("Of(dead) = %v, want OutcomeDead", got)
	}
	if got := Of(connect.NewError(connect.CodeNotFound, errors.New("no tile"))); got != OutcomeRejected {
		t.Fatalf("Of(plain not_found) = %v, want OutcomeRejected", got)
	}
	if got := ReactRead(OutcomeDead); got != inflight.Dead {
		t.Errorf("ReactRead(dead) = %v, want inflight.Dead", got)
	}
	if ReadSurfaces(OutcomeDead) || ReadSurfaces(OutcomeOK) || !ReadSurfaces(OutcomeRejected) || !ReadSurfaces(OutcomeTransport) {
		t.Error("a read surfaces every failure except the dead verdict")
	}
	if got, want := ReactPreview(deadAnswer(), false), (PreviewReaction{Latch: inflight.Dead}); got != want {
		t.Errorf("ReactPreview(dead) = %+v, want %+v", got, want)
	}
	if g := ReactGridRead("a", "", OutcomeDead); g.Latch != inflight.Dead || g.Store {
		t.Errorf("ReactGridRead(dead) = %+v", g)
	}
	for _, react := range []func(Outcome) Reaction{React, ReactOptimistic, ReactSave} {
		if got, want := react(OutcomeDead), react(OutcomeRejected); got != want {
			t.Errorf("a write meeting the dead verdict = %+v, want the rejection %+v", got, want)
		}
	}
}

func TestReactPreviewTable(t *testing.T) {
	unimpl := connect.NewError(connect.CodeUnimplemented, errors.New("no previews"))
	down := connect.NewError(connect.CodeUnavailable, errors.New("dark"))
	verdict := connect.NewError(connect.CodeNotFound, errors.New("no tile"))
	cases := []struct {
		name  string
		err   error
		empty bool
		want  PreviewReaction
	}{
		{"bytes are the face", nil, false, PreviewReaction{Store: true}},
		{"an empty answer settles the miss", nil, true, PreviewReaction{Settle: true}},
		{"no previews served settles it too", unimpl, false, PreviewReaction{Settle: true}},
		{"a dark source surfaces and latches until the backstop", down, false, PreviewReaction{Surface: true, Latch: inflight.Unreachable}},
		{"a verdict surfaces and latches until a change", verdict, false, PreviewReaction{Surface: true, Latch: inflight.Refused}},
	}
	for _, c := range cases {
		if got := ReactPreview(c.err, c.empty); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

// A pane standing in a tile that is gone has no dead face: the grid draws a
// dead link, but a place draws an empty box, so there the verdict is said.
func TestAPlaceReadSaysTheDeadVerdict(t *testing.T) {
	for v, want := range map[inflight.Verdict]bool{
		inflight.Answered:    false,
		inflight.Unreachable: false,
		inflight.Refused:     true,
		inflight.Dead:        true,
	} {
		if got := PlaceReadSurfaces(v); got != want {
			t.Errorf("PlaceReadSurfaces(%v) = %v, want %v", v, got, want)
		}
	}
}

// A link whose target is gone draws dead and says nothing: the read that
// found it out is a link's, not a place's.
func TestATargetReadSaysNothingOfTheDeadVerdict(t *testing.T) {
	for v, want := range map[inflight.Verdict]bool{
		inflight.Answered:    false,
		inflight.Unreachable: false,
		inflight.Refused:     true,
		inflight.Dead:        false,
	} {
		if got := TargetReadSurfaces(v); got != want {
			t.Errorf("TargetReadSurfaces(%v) = %v, want %v", v, got, want)
		}
	}
}

// disabledAnswer is the node's disabled verdict as the browser receives it,
// through the one codec that rebuilds an error.
func disabledAnswer() error {
	off := gwerr.SourceDisabled("n/rtb", "rtb is disabled until the node restarts")
	return gwerr.ConnectDetails(off, connect.NewError(gwerr.ConnectCode(status.Code(off)), errors.New(status.Convert(off).Message())))
}

// A write to a source the user disabled is a verdict that cannot change before
// the node restarts: never parked, never retried. An optimistic write (a pan,
// a drag) is dropped without a word, since the source's chip already says it
// is off; a save, which drops the user's words, says why; a plain
// FailedPrecondition stays a conflict.
func TestTheDisabledVerdict(t *testing.T) {
	if got := Of(disabledAnswer()); got != OutcomeDisabled {
		t.Fatalf("Of(disabled) = %v, want OutcomeDisabled", got)
	}
	if got := Of(connect.NewError(connect.CodeFailedPrecondition, errors.New("version"))); got != OutcomeConflict {
		t.Fatalf("Of(plain failed_precondition) = %v, want OutcomeConflict", got)
	}
	if Unheard(OutcomeDisabled) {
		t.Fatal("the disabled verdict reads as unheard, so the outbox would park it")
	}
	for name, r := range map[string]Reaction{
		"React": React(OutcomeDisabled), "ReactOptimistic": ReactOptimistic(OutcomeDisabled), "ReactSave": ReactSave(OutcomeDisabled),
	} {
		if r.Retry {
			t.Errorf("%s retries the disabled verdict: %+v", name, r)
		}
	}
	if got, want := ReactOptimistic(OutcomeDisabled), (Reaction{DropLocal: true}); got != want {
		t.Errorf("ReactOptimistic(disabled) = %+v, want %+v: dropped, no notice, no refetch", got, want)
	}
	if got := NoticesFor(ReactOptimistic(OutcomeDisabled), OutcomeDisabled, true); got != (Notices{}) {
		t.Errorf("an optimistic write meeting the disabled verdict posts %+v, want nothing", got)
	}
	if got, want := ReactSave(OutcomeDisabled), ReactSave(OutcomeRejected); got != want {
		t.Errorf("ReactSave(disabled) = %+v, want the rejection %+v: the user's words are dropped, so it is said", got, want)
	}
	if got, want := React(OutcomeDisabled), (Reaction{Log: true}); got != want {
		t.Errorf("React(disabled) = %+v, want %+v: a gesture refused says why", got, want)
	}
}
