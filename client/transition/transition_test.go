package transition

import (
	"reflect"
	"testing"

	"github.com/josephburnett/gridwell/client/pane"
)

// rig is a fake shim: it records every segment installed on a pane and every
// landing that ran, in order.
type rig struct {
	set     *Set
	entered []string
	landed  []string
	places  map[string]pane.Stack
	// activeAtLanding records, per pane, what Active said while its landing
	// ran — the question a framing write asks.
	activeAtLanding map[string]bool
}

func newRig() *rig {
	r := &rig{places: map[string]pane.Stack{}, activeAtLanding: map[string]bool{}}
	r.set = New(
		func(paneID string, seg Segment) {
			r.entered = append(r.entered, paneID)
			if seg.Place != nil {
				r.places[paneID] = seg.Place.Clone()
			}
		},
		func(t *Transition) {
			r.landed = append(r.landed, t.PaneID)
			r.activeAtLanding[t.PaneID] = r.set.Active(t.PaneID)
			if t.OnComplete != nil {
				t.OnComplete()
			}
		},
	)
	return r
}

// descentInto is a one-leg descent whose landing is the frame push: done
// records that the descent actually happened.
func descentInto(paneID, tileID string, done *bool) *Transition {
	outer := pane.NewStack("g1")
	return &Transition{
		PaneID:   paneID,
		Segments: []Segment{{Place: &outer, FromZoom: 1, ToZoom: 4, DurationMs: 350}},
		OnComplete: func() {
			outer.Push(pane.ContentFrame(tileID, pane.Footprint{W: 1, H: 1}, 4, "", 0, 0))
			*done = true
		},
	}
}

// A transition belongs to a pane. Starting one in pane B must not void pane
// A's — A must still reach its own landing.
func TestOneTransitionPerPaneNotOnePerApp(t *testing.T) {
	r := newRig()
	var aDone, bDone bool
	r.set.Start(descentInto("A", "g1/7", &aDone), 0)
	r.set.Start(descentInto("B", "g1/8", &bDone), 10)

	if !r.set.Active("A") || !r.set.Active("B") {
		t.Fatal("B's transition displaced A's: one slot, not one per pane")
	}
	r.set.Advance("A", 400)
	if !aDone {
		t.Fatal("A's descent never landed")
	}
	r.set.Advance("B", 400)
	if !bDone {
		t.Fatal("B's descent never landed")
	}
	if r.set.Any() {
		t.Fatalf("both landed, nothing should be live: %v", r.set.List())
	}
}

// A displaced transition lands: the pane arrives where it was going, and the
// landing that pushes the frame runs, before the new one starts.
func TestStartingAgainOnTheSamePaneLandsTheOutgoing(t *testing.T) {
	r := newRig()
	var firstDone, secondDone bool
	r.set.Start(descentInto("A", "g1/7", &firstDone), 0)
	r.set.Start(descentInto("A", "g1/8", &secondDone), 10)
	if !firstDone {
		t.Fatal("the displaced descent was voided after visibly animating")
	}
	if secondDone {
		t.Fatal("the incoming transition landed before it ran")
	}
	if got := r.landed; !reflect.DeepEqual(got, []string{"A"}) {
		t.Fatalf("landings: %v", got)
	}
	r.set.Advance("A", 400)
	if !secondDone {
		t.Fatal("the incoming descent never landed")
	}
}

// Cancel is the one clearing door: it installs the destination and runs the
// landing, instead of dropping the pane on the animation's scratch state.
func TestCancelInstallsTheDestinationAndRunsTheLanding(t *testing.T) {
	r := newRig()
	var done bool
	tr := descentInto("A", "g1/7", &done)
	// A second leg, so "the final segment" is not also the first.
	final := pane.NewStack("g2")
	tr.Segments = append(tr.Segments, Segment{
		Place: &final, FromZoom: 0.5, ToZoom: 2, ToCx: 9, ToCy: 3, DurationMs: 350,
	})
	r.set.Start(tr, 0)

	if !r.set.Cancel("A") {
		t.Fatal("Cancel found nothing to cancel")
	}
	if !done {
		t.Fatal("a cancelled transition must still run its landing")
	}
	if got := r.places["A"].GridID; got != "g2" {
		t.Fatalf("cancel landed on %q, not the final segment's place", got)
	}
	if r.set.Active("A") {
		t.Fatal("a cancelled transition is not still live")
	}
	if r.set.Cancel("A") {
		t.Fatal("cancelling twice must not land twice")
	}
}

// Cancel installs the END of the final segment, not its start: a cancel is a
// jump to the destination, not a snap back to where the leg began.
func TestCancelJumpsToTheEndOfTheMotion(t *testing.T) {
	var got Segment
	set := New(func(_ string, seg Segment) { got = seg }, func(*Transition) {})
	place := pane.NewStack("g1")
	set.Start(&Transition{PaneID: "A", Segments: []Segment{{
		Place:  &place,
		FromCx: 1, FromCy: 2, FromZoom: 3,
		ToCx: 10, ToCy: 20, ToZoom: 30,
		DurationMs: 350,
	}}}, 0)
	set.Cancel("A")
	if got.FromCx != 10 || got.FromCy != 20 || got.FromZoom != 30 {
		t.Fatalf("cancel installed %v,%v,%v — not the destination", got.FromCx, got.FromCy, got.FromZoom)
	}
}

// The landing runs with the pane no longer animating, so a framing write made
// from inside it persists the real destination rather than being refused as
// mid-animation scratch.
func TestALandingSeesAPaneThatIsNoLongerAnimating(t *testing.T) {
	r := newRig()
	var done bool
	r.set.Start(descentInto("A", "g1/7", &done), 0)
	r.set.Advance("A", 400)
	if r.activeAtLanding["A"] {
		t.Fatal("Active was still true inside the landing")
	}

	r.set.Start(descentInto("B", "g1/8", &done), 0)
	r.set.Cancel("B")
	if r.activeAtLanding["B"] {
		t.Fatal("Active was still true inside a cancelled transition's landing")
	}
}

// A landing that starts the next transition — a descent chained onto an
// ascent — must not recurse into the one it came from.
func TestALandingMayStartTheNextTransition(t *testing.T) {
	r := newRig()
	var chained bool
	first := descentInto("A", "g1/7", new(bool))
	first.OnComplete = func() {
		r.set.Start(descentInto("A", "g1/8", &chained), 500)
	}
	r.set.Start(first, 0)
	r.set.Advance("A", 400)
	if !r.set.Active("A") {
		t.Fatal("the chained transition is not live")
	}
	if got := r.landed; !reflect.DeepEqual(got, []string{"A"}) {
		t.Fatalf("the chained start re-landed the outgoing one: %v", got)
	}
	if chained {
		t.Fatal("the chained transition landed immediately")
	}
}

// CancelAll lands every pane; Drop lands none, because a pane that is going
// away has nowhere to land.
func TestCancelAllLandsEveryPaneAndDropLandsNone(t *testing.T) {
	r := newRig()
	var a, b, c bool
	r.set.Start(descentInto("A", "g1/1", &a), 0)
	r.set.Start(descentInto("B", "g1/2", &b), 0)
	r.set.CancelAll()
	if !a || !b || r.set.Any() {
		t.Fatalf("CancelAll left something behind: a=%v b=%v live=%v", a, b, r.set.List())
	}
	r.set.Start(descentInto("C", "g1/3", &c), 0)
	r.set.Drop("C")
	if c {
		t.Fatal("a dropped pane's landing must not run")
	}
	if r.set.Any() {
		t.Fatal("Drop must retire the transition")
	}
}

// Multi-segment advance walks the legs and lands once at the end.
func TestAdvanceWalksTheSegments(t *testing.T) {
	r := newRig()
	one := pane.NewStack("g1")
	two := pane.NewStack("g2")
	r.set.Start(&Transition{PaneID: "A", Segments: []Segment{
		{Place: &one, DurationMs: 100},
		{Place: &two, DurationMs: 100},
	}}, 0)
	if !r.set.Advance("A", 100) {
		t.Fatal("the first advance must move to the second segment, not land")
	}
	if r.places["A"].GridID != "g2" {
		t.Fatalf("second segment's place not installed: %q", r.places["A"].GridID)
	}
	if r.set.Advance("A", 200) {
		t.Fatal("the last advance must land")
	}
	if got := r.landed; !reflect.DeepEqual(got, []string{"A"}) {
		t.Fatalf("landings: %v", got)
	}
	if r.set.Advance("A", 300) {
		t.Fatal("advancing a landed transition must do nothing")
	}
}
