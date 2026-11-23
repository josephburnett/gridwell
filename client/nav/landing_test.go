package nav

import (
	"slices"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/zoomtrans"
)

// atOrigin lands an 800 by 600 pane at the child grid's origin, zoom 1: at 64
// px a cell it shows cells -6 to 6 across and -4 to 4 down.
func atOrigin(t *testing.T) landing {
	t.Helper()
	v, err := rpc.NewFraming(0, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	return landing{Rect: pane.Rect{W: 800, H: 600}, View: v}
}

func landingWorld() World {
	w := baseWorld(gridPane("pane1", "g1"))
	w.TextScale = 1
	return w
}

func reads(p Plan) []string {
	var out []string
	for _, e := range p.Effects {
		switch e.Kind {
		case EffFetchTileContent:
			out = append(out, "body "+e.TileID)
		case EffFetchPreview:
			out = append(out, "preview "+e.TileID)
		case EffFetchGrid:
			out = append(out, "grid "+e.GridID)
		}
	}
	slices.Sort(out)
	return out
}

// The landing paint's second layer, asked before the landing: what the paint
// would ask of each tile it shows, and nothing of a tile off screen.
func TestLandingReadsWhatTheLandingPaintShows(t *testing.T) {
	tile := func(id, kind string, x, y, w, h int64) *gridwellv1.Tile {
		return &gridwellv1.Tile{Id: id, Kind: kind, GridId: "g2", X: x, Y: y, W: w, H: h}
	}
	text := tile("t1", rpc.KindText, 0, 0, 2, 2)
	far := tile("t2", rpc.KindText, 40, 40, 2, 2)
	info := tile("t3", rpc.KindText, 2, 0, 2, 2)
	info.TextPresentation = rpc.TextPresentationInfo
	link := tile("t4", rpc.KindText, -2, 0, 2, 2)
	link.Link, link.LinkTargetId = true, "u1/9"
	url := tile("r1", rpc.KindURL, 0, 2, 2, 2)
	url.PreviewBlobId = 7
	blank := tile("r2", rpc.KindURL, 2, 2, 2, 2)
	shell := tile("s1", rpc.KindShell, -2, 2, 2, 2)
	shell.PreviewBlobId = -3
	door := tile("d1", rpc.KindDoor, -4, -2, 2, 2)
	door.ChildGridId = "g3"
	held := tile("d2", rpc.KindDoor, 4, -2, 2, 2)
	held.ChildGridId = "g4"
	ws := tile("w1", rpc.KindWorkspace, 0, -3, 2, 2)

	w := landingWorld()
	w.Current = currentOf("g4")
	var pl planner
	pl.landingReads(w, &GridRows{Tiles: []*gridwellv1.Tile{text, far, info, link, url, blank, shell, door, held, ws}, Writable: true}, atOrigin(t))
	want := []string{"body t1", "body w1", "grid g3", "preview r1", "preview s1"}
	if got := reads(pl.plan()); !slices.Equal(got, want) {
		t.Fatalf("landing reads = %v, want %v", got, want)
	}
	if e := only(t, pl.plan(), EffFetchGrid); !e.Preview {
		t.Fatal("a door's grid is asked as its preview")
	}
}

// A text too short at the landing to show a body line under its name is
// not read, as its paint does not read it.
func TestLandingReadsNoBodyATextCannotShow(t *testing.T) {
	small := &gridwellv1.Tile{Id: "t1", Kind: rpc.KindText, GridId: "g2", W: 1, H: 1}
	v, err := rpc.NewFraming(0, 0, 0.1)
	if err != nil {
		t.Fatal(err)
	}
	var pl planner
	pl.landingReads(landingWorld(), &GridRows{Tiles: []*gridwellv1.Tile{small}}, landing{Rect: pane.Rect{W: 800, H: 600}, View: v})
	if got := reads(pl.plan()); len(got) != 0 {
		t.Fatalf("landing reads = %v, want none", got)
	}
}

// descentDoor and the tile at the centre of its landing.
func descentDoor(t *testing.T) (*gridwellv1.Tile, *gridwellv1.Tile) {
	t.Helper()
	door := &gridwellv1.Tile{Id: "w1", Kind: rpc.KindDoor, GridId: "g1", X: 2, Y: 3, W: 4, H: 4, ChildGridId: "g2"}
	size, _ := zoomtrans.SizeOf(800, 600)
	_, _, final, ok := zoomtrans.Descent(zoomtrans.Endpoints{Cx: 0, Cy: 0, Zoom: 1},
		zoomtrans.DoorOf(door), size, 64)
	if !ok {
		t.Fatal("no descent")
	}
	body := &gridwellv1.Tile{Id: "t1", Kind: rpc.KindText, GridId: "g2",
		X: int64(final.Cx) - 1, Y: int64(final.Cy) - 1, W: 3, H: 3}
	return door, body
}

// A descent into a grid held current reads its landing's second layer at the
// gesture, under the transition.
func TestDescentIntoAHeldGridReadsItsLandingAtTheGesture(t *testing.T) {
	door, body := descentDoor(t)
	w := landingWorld()
	w.Current = currentOf("g2")
	w.Door = &DoorWorld{Child: &GridRows{Tiles: []*gridwellv1.Tile{body}, Writable: true}}
	plan := New().Do(descendGesture("pane1", door), w)
	if got := reads(plan); !slices.Equal(got, []string{"body t1"}) {
		t.Fatalf("reads at the gesture = %v, want the body", got)
	}
}

// A descent into a grid not held current waits for the grid's read, then
// reads its landing's second layer, while the pane is still on its way.
func TestDescentIntoAGridNotHeldReadsItsLandingWhenTheGridLands(t *testing.T) {
	door, body := descentDoor(t)
	m := New()
	w := landingWorld()
	w.Door = &DoorWorld{}
	plan := m.Do(descendGesture("pane1", door), w)
	if got := reads(plan); !slices.Equal(got, []string{"grid g2"}) {
		t.Fatalf("reads at the gesture = %v, want the grid alone", got)
	}
	aw := only(t, plan, EffAwait)
	if aw.Request.Kind != RequestGetGrid || aw.Request.ID != "g2" {
		t.Fatalf("awaits %+v, want the grid's read", aw.Request)
	}
	rows := &GridRows{Tiles: []*gridwellv1.Tile{body}, Writable: true}

	moving := landingWorld()
	moving.Animating["pane1"] = true
	if got := reads(m.Resume(aw.Token, Result{OK: true, Grid: rows}, moving)); !slices.Equal(got, []string{"body t1"}) {
		t.Fatalf("reads when the grid landed = %v, want the body", got)
	}

	m = New()
	aw = only(t, m.Do(descendGesture("pane1", door), w), EffAwait)
	if got := reads(m.Resume(aw.Token, Result{OK: true, Grid: rows}, landingWorld())); len(got) != 0 {
		t.Fatalf("a pane that went back reads %v for nobody", got)
	}
}
