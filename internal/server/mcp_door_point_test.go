package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/shellstream"
	"github.com/josephburnett/gridwell/client/shellws"
	"github.com/josephburnett/gridwell/internal/mcpdoor"
)

// viewer is a client session showing the node: its event stream open and its
// view said, as the wasm client does at boot.
func viewer(t *testing.T, hs *httptest.Server, label string, panes ...*gridwellv1.ViewPane) *rpc.Client {
	t.Helper()
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := cl.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = stream.Close() })
	say(t, cl, label, panes...)
	return cl
}

func say(t *testing.T, cl *rpc.Client, label string, panes ...*gridwellv1.ViewPane) {
	t.Helper()
	// The book stamps a change with the clock; keep two views' stamps apart.
	time.Sleep(2 * time.Millisecond)
	if err := cl.SetView(context.Background(), &gridwellv1.SetViewRequest{Label: label, Panes: panes}); err != nil {
		t.Fatal(err)
	}
}

func focusedOn(grid string) *gridwellv1.ViewPane {
	return &gridwellv1.ViewPane{Id: "p1", Anchor: grid, GridId: grid, Focused: true}
}

func agentAt(t *testing.T, hs *httptest.Server, hdr map[string]string) *mcp.ClientSession {
	t.Helper()
	h := bearer(testPassword)
	for k, v := range hdr {
		h[k] = v
	}
	s, err := (&mcpNode{url: hs.URL + mcpdoor.Path, web: hs.Client()}).agent(t, h)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// pointed names, beside the screen it chose, every other screen showing the
// node with each pane's place, and leaves out another node telling its
// share, which shows no screen.
func TestMCPPointedNamesEveryOtherScreenWithItsPanes(t *testing.T) {
	f := newShellDoorFixture(t, Config{})
	cl := viewer(t, f.hs, "desktop", focusedOn(f.root),
		&gridwellv1.ViewPane{Id: "p2", Anchor: f.root, Path: []string{"k1"}, ContentId: "c1", GridId: f.root})
	far := viewer(t, f.hs, "node")
	if err := far.SetView(context.Background(), &gridwellv1.SetViewRequest{Label: "node", ShownGridIds: []string{f.root}}); err != nil {
		t.Fatal(err)
	}
	phone := viewer(t, f.hs, "browser", focusedOn(f.root))

	var p mcpdoor.PointedOut
	call(t, agentAt(t, f.hs, nil), "pointed", nil, &p)
	if p.Session != phone.Session() || len(p.Panes) != 1 || !p.Panes[0].Focused || p.Changed == "" {
		t.Fatalf("pointed = %+v, want the browser %s with its one focused pane", p, phone.Session())
	}
	if len(p.Others) != 1 {
		t.Fatalf("others = %+v, want the one desktop session and not the node's", p.Others)
	}
	o := p.Others[0]
	if o.ID != cl.Session() || o.Label != "desktop" || o.Changed == "" || len(o.Panes) != 2 {
		t.Fatalf("other = %+v, want %s labelled desktop with two panes", o, cl.Session())
	}
	if q := o.Panes[0]; !q.Focused || q.Grid != f.root || q.Anchor != f.root {
		t.Errorf("pane 1 = %+v, want focused on %s", q, f.root)
	}
	if q := o.Panes[1]; q.Focused || q.Content != "c1" || !slices.Equal(q.Path, []string{"k1"}) {
		t.Errorf("pane 2 = %+v, want inside c1 by path [k1], unfocused", q)
	}
}

// pointed answers the focused pane's grid's selection when it has one, the
// tiles as read_grid answers them, and the grid itself when it has none.
func TestMCPPointedIsTheSelectionElseTheGrid(t *testing.T) {
	f := newShellDoorFixture(t, Config{})
	s := agentAt(t, f.hs, nil)
	if res := callRaw(t, s, "pointed", nil); !res.IsError || !strings.HasPrefix(toolText(res), "no session:") {
		t.Fatalf("with no screen open, pointed = %q, want a plain no session", toolText(res))
	}
	cl := viewer(t, f.hs, "browser", focusedOn(f.root))
	mkText(t, s, f.root, "a")
	b := mkText(t, s, f.root, "b")

	var p mcpdoor.PointedOut
	call(t, s, "pointed", nil, &p)
	if p.Session != cl.Session() || p.ChosenBy != "most recent" || p.Points != "grid" || p.Grid.ID != f.root || len(p.Tiles) != 0 {
		t.Fatalf("pointed = %+v, want the grid %s itself", p, f.root)
	}

	var g mcpdoor.GridOut
	call(t, s, "select", map[string]any{"grid": f.root, "add": []string{b.ID}}, &g)
	call(t, s, "pointed", nil, &p)
	if p.Points != "selection" || len(p.Tiles) != 1 || p.Tiles[0].ID != b.ID || p.Tiles[0].Name != "b" {
		t.Fatalf("pointed = %+v, want the selection [%s] as read_grid answers it", p, b.ID)
	}
}

// paneTileView is a desktop session inside a pane tile whose layout holds two
// panes on root, each with its own set: the first pane's sel1, the focused
// second's sel2. The layout is written and the view said as the client does
// both, through pane.EncodeLayout and pane.View.
func paneTileView(t *testing.T, f *shellDoorFixture, sel1, sel2 []string) (*rpc.Client, *gridwellv1.Tile) {
	t.Helper()
	ctx := context.Background()
	pt, err := f.cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: f.root, Tile: &gridwellv1.Tile{Kind: rpc.KindPane, X: 5, Y: 5, W: 1, H: 1, AltText: "work"}})
	if err != nil {
		t.Fatal(err)
	}
	tr := pane.TreeAtPlace("w1:", f.root, nil, rpc.ViewOf(0, 0, 1))
	first := tr.FocusedPane()
	second, err := tr.Split(pane.Vertical)
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.SetFocus(second.ID); err != nil {
		t.Fatal(err)
	}
	first.Select(sel1)
	second.Select(sel2)
	data, _, err := pane.EncodeLayout(tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.cl.WriteContent(ctx, pt.Id, rpc.ContentBasis{Version: pt.Version}, data); err != nil {
		t.Fatal(err)
	}
	var lv pane.Levels
	lv.Push(pane.Level{TileID: pt.Id, GridID: f.root})
	v := pane.View(tr, &lv, pane.Layout(tr, pane.Rect{W: 800, H: 600}), "desktop",
		func(string, []string) string { return f.root }, func(string) map[string]*gridwellv1.Tile { return nil })
	cl := viewer(t, f.hs, "desktop")
	time.Sleep(2 * time.Millisecond)
	if err := cl.SetView(ctx, v); err != nil {
		t.Fatal(err)
	}
	return cl, pt
}

// Inside a pane tile the focused pane's set is its own in the layout, not the
// grid row's: pointed answers that set, and names the pane tile on every pane.
func TestMCPPointedInsideAPaneTileIsThatPanesSet(t *testing.T) {
	f := newShellDoorFixture(t, Config{})
	s := agentAt(t, f.hs, nil)
	a := mkText(t, s, f.root, "a")
	b := mkText(t, s, f.root, "b")
	c := mkText(t, s, f.root, "c")
	var g mcpdoor.GridOut
	call(t, s, "select", map[string]any{"grid": f.root, "add": []string{a.ID}}, &g)
	cl, pt := paneTileView(t, f, []string{c.ID}, []string{b.ID})

	var p mcpdoor.PointedOut
	call(t, s, "pointed", nil, &p)
	if p.Session != cl.Session() || p.PaneTile != pt.Id || p.SetIn != "pane tile" || p.Points != "selection" ||
		len(p.Tiles) != 1 || p.Tiles[0].ID != b.ID {
		t.Fatalf("pointed = %+v, want the focused pane's own set [%s] in pane tile %s, not the row's [%s]", p, b.ID, pt.Id, a.ID)
	}
	for _, sp := range p.Panes {
		if sp.PaneTile != pt.Id {
			t.Errorf("pointed pane %+v, want it inside pane tile %s", sp, pt.Id)
		}
	}
}

// A pane inside a pane tile with no set of its own points at the grid, though
// the grid row has a set: the row's set is not the one that pane shows.
func TestMCPPointedInsideAPaneTileIgnoresTheRowSet(t *testing.T) {
	f := newShellDoorFixture(t, Config{})
	s := agentAt(t, f.hs, nil)
	a := mkText(t, s, f.root, "a")
	var g mcpdoor.GridOut
	call(t, s, "select", map[string]any{"grid": f.root, "add": []string{a.ID}}, &g)
	paneTileView(t, f, []string{a.ID}, nil)

	var p mcpdoor.PointedOut
	call(t, s, "pointed", nil, &p)
	if p.Points != "grid" || p.Grid.ID != f.root || len(p.Tiles) != 0 || p.SetIn != "pane tile" {
		t.Fatalf("pointed = %+v, want the grid %s itself, the row's set not being this pane's", p, f.root)
	}
}

// A view naming a pane the layout does not hold where the view has it is
// ahead of the layout: not loaded, never the row's set in its place.
func TestMCPPointedInsideAPaneTileAheadOfItsLayoutIsNotLoaded(t *testing.T) {
	f := newShellDoorFixture(t, Config{})
	s := agentAt(t, f.hs, nil)
	cl, pt := paneTileView(t, f, nil, nil)
	say := &gridwellv1.SetViewRequest{Label: "desktop", PaneTiles: []string{pt.Id},
		Panes: []*gridwellv1.ViewPane{{Id: "w1:p9", Anchor: f.root, GridId: f.root, Focused: true, LayoutPaneId: "p9"}}}
	if err := cl.SetView(context.Background(), say); err != nil {
		t.Fatal(err)
	}
	refusedWith(t, callRaw(t, s, "pointed", nil), "not loaded:")
}

// A session named is the one answered; unnamed, the one whose view changed
// last, with the others named so the agent can ask.
func TestMCPPointedChoosesTheNamedElseTheMostRecentSession(t *testing.T) {
	f := newShellDoorFixture(t, Config{})
	older := viewer(t, f.hs, "desktop", focusedOn(f.root))
	newer := viewer(t, f.hs, "browser", focusedOn(f.root))
	s := agentAt(t, f.hs, nil)

	var p mcpdoor.PointedOut
	call(t, s, "pointed", nil, &p)
	if p.Session != newer.Session() || p.ChosenBy != "most recent" || len(p.Others) != 1 || p.Others[0].ID != older.Session() {
		t.Fatalf("pointed = %+v, want %s, naming %s as the other", p, newer.Session(), older.Session())
	}
	call(t, s, "pointed", map[string]any{"session": older.Session()}, &p)
	if p.Session != older.Session() || p.ChosenBy != "argument" || p.Label != "desktop" {
		t.Fatalf("pointed(session) = %+v, want the session named", p)
	}
	refusedWith(t, callRaw(t, s, "pointed", map[string]any{"session": "nobody"}), "not found:")

	// Saying its view again moves the older session to the front.
	say(t, older, "desktop", focusedOn(f.root), &gridwellv1.ViewPane{Id: "p2", Anchor: f.root, GridId: f.root})
	call(t, s, "pointed", nil, &p)
	if p.Session != older.Session() {
		t.Errorf("after the desktop's view changed, pointed chose %s, want %s", p.Session, older.Session())
	}
}

// A screen whose stream closed, as a phone's does in the background, is still
// pointed at, marked away with a note saying since when; once another screen
// is open, the open one wins and the away one is among the others.
func TestMCPPointedSeesAnAwayScreenUntilAnOpenOne(t *testing.T) {
	f := newShellDoorFixture(t, Config{})
	phone := rpc.NewClient(f.hs.Client(), f.hs.URL, connect.WithProtoJSON())
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := phone.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	say(t, phone, "browser", focusedOn(f.root))
	s := agentAt(t, f.hs, nil)
	var p mcpdoor.PointedOut
	call(t, s, "pointed", nil, &p)
	if p.Session != phone.Session() || p.AwaySince != "" {
		t.Fatalf("pointed = %+v, want the open browser %s", p, phone.Session())
	}

	cancel()
	_ = stream.Close()
	deadline := time.Now().Add(3 * time.Second)
	for call(t, s, "pointed", nil, &p); p.AwaySince == ""; call(t, s, "pointed", nil, &p) {
		if time.Now().After(deadline) {
			t.Fatalf("after the browser's stream closed, pointed = %+v, want it away", p)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if p.Session != phone.Session() || !strings.Contains(p.Note, "away since "+p.AwaySince) || p.Grid.ID != f.root {
		t.Fatalf("pointed = %+v, want the away browser %s on %s, the note saying since when", p, phone.Session(), f.root)
	}

	desk := viewer(t, f.hs, "desktop", focusedOn(f.root))
	// Its stream is counted once the node is listening; an answer leaves out
	// what it omits, so each lands in a fresh value.
	deadline = time.Now().Add(3 * time.Second)
	for {
		p = mcpdoor.PointedOut{}
		call(t, s, "pointed", nil, &p)
		if p.Session == desk.Session() || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if p.Session != desk.Session() || p.AwaySince != "" || len(p.Others) != 1 || p.Others[0].ID != phone.Session() || p.Others[0].AwaySince == "" {
		t.Fatalf("pointed = %+v, want the open desktop %s, naming the away browser among the others", p, desk.Session())
	}
}

// attach goes live on a shell tile from cl's session, as the wasm client's
// shell stack does.
func attach(t *testing.T, f *shellDoorFixture, cl *rpc.Client, tile string) {
	t.Helper()
	reg := shellstream.New(shellws.Dialer(shellws.Options{Origin: f.hs.URL, HTTPClient: f.hs.Client(), Session: cl.Session()}),
		func(string, []byte) {}, func(shellstream.Exit) {})
	reg.Open(tile, tile, 80, 24)
	t.Cleanup(func() { reg.Close(tile) })
}

// The X-Gridwell-Shell header names the agent's shell, and pointed answers
// the session holding it live now, the newest attachment after a takeover. A
// shell live nowhere falls back to the most recent session and says so.
func TestMCPPointedFollowsTheShellTheAgentRunsIn(t *testing.T) {
	f := newShellDoorFixture(t, Config{})
	tile := f.createShell(t, 0, 0)
	desk := viewer(t, f.hs, "desktop", focusedOn(f.root))
	attach(t, f, desk, tile.Id)
	waitSession(t, f.fake)
	phone := viewer(t, f.hs, "browser", focusedOn(f.root))

	key := rpc.LocalOf(rpc.ShellSession(tile))
	s := agentAt(t, f.hs, map[string]string{mcpdoor.ShellHeader: key})
	var p mcpdoor.PointedOut
	call(t, s, "pointed", nil, &p)
	if p.Session != desk.Session() || p.ChosenBy != "your shell" {
		t.Fatalf("pointed = %+v, want %s, whose pane holds shell %s, over the more recent %s", p, desk.Session(), key, phone.Session())
	}

	attach(t, f, phone, tile.Id)
	deadline := time.Now().Add(3 * time.Second)
	for call(t, s, "pointed", nil, &p); p.Session != phone.Session(); call(t, s, "pointed", nil, &p) {
		if time.Now().After(deadline) {
			t.Fatalf("after the browser took the shell over, pointed = %+v, want %s", p, phone.Session())
		}
		time.Sleep(10 * time.Millisecond)
	}

	dead := agentAt(t, f.hs, map[string]string{mcpdoor.ShellHeader: "zzzzzzz"})
	call(t, dead, "pointed", nil, &p)
	if p.Session != phone.Session() || p.ChosenBy != "most recent" || !strings.Contains(p.Note, "zzzzzzz") {
		t.Fatalf("pointed with a shell live nowhere = %+v, want the most recent session and a note naming the shell", p)
	}
}

// select adds and removes without a claim, and answers the grid as read_grid
// does: a select made while the user's set moved merges with it, a tile off
// the grid is ignored, a recursive clear reaches the grids inside wells, and
// the all-clear reaches every namespace.
func TestMCPSelectAddsAndRemovesWithoutAClaim(t *testing.T) {
	n := newMCPNode(t)
	s, err := n.agent(t, bearer(testPassword))
	if err != nil {
		t.Fatal(err)
	}
	a := mkText(t, s, n.root, "a")
	b := mkText(t, s, n.root, "b")
	var g mcpdoor.GridOut
	call(t, s, "read_grid", map[string]any{"id": n.root}, &g)
	if g.Grid.Selection == nil || len(g.Grid.Selection) != 0 {
		t.Fatalf("read_grid selection = %v, want an empty set", g.Grid.Selection)
	}

	call(t, s, "select", map[string]any{"grid": n.root, "add": []string{a.ID}}, &g)
	// The user, meanwhile, selects b on their screen.
	page := map[string]string{"Cookie": AuthCookieName + "=" + AuthToken(testPassword), "Origin": n.connectURL}
	cl := rpc.NewClient(&http.Client{Transport: headers{h: page, base: n.web.Transport}}, n.connectURL, connect.WithProtoJSON())
	if err := cl.SetSelection(context.Background(), &gridwellv1.SetSelectionRequest{GridId: n.root, Add: []string{b.ID}}); err != nil {
		t.Fatal(err)
	}
	call(t, s, "read_grid", map[string]any{"id": n.root}, &g)
	if got := slices.Sorted(slices.Values(g.Grid.Selection)); !slices.Equal(got, slices.Sorted(slices.Values([]string{a.ID, b.ID}))) {
		t.Fatalf("read_grid selection = %v, want both tiles", got)
	}
	call(t, s, "select", map[string]any{"grid": n.root, "remove": []string{a.ID}, "add": []string{a.ID + "999"}}, &g)
	if !slices.Equal(g.Grid.Selection, []string{b.ID}) {
		t.Fatalf("after removing a, selection = %v, want [%s]", g.Grid.Selection, b.ID)
	}

	// clear with recurse empties this grid and every grid inside its wells;
	// recurse alone is refused.
	well, err := cl.CreateTile(context.Background(), &gridwellv1.CreateTileRequest{GridId: n.root,
		Tile: &gridwellv1.Tile{Kind: rpc.KindWell, X: 9, Y: 9, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	inner := mkText(t, s, well.ChildGridId, "inner")
	call(t, s, "select", map[string]any{"grid": well.ChildGridId, "add": []string{inner.ID}}, &g)
	if len(g.Grid.Selection) != 1 {
		t.Fatalf("the inner grid's selection = %v", g.Grid.Selection)
	}
	refusedWith(t, callRaw(t, s, "select", map[string]any{"grid": n.root, "recurse": true}), "refused:")
	call(t, s, "select", map[string]any{"grid": n.root, "clear": true, "recurse": true}, &g)
	if len(g.Grid.Selection) != 0 {
		t.Fatalf("after a recursive clear the grid holds %v", g.Grid.Selection)
	}
	call(t, s, "read_grid", map[string]any{"id": well.ChildGridId}, &g)
	if len(g.Grid.Selection) != 0 {
		t.Fatalf("after a recursive clear the inner grid holds %v", g.Grid.Selection)
	}

	// all clears every grid the node holds, the feed plugin's included.
	call(t, s, "select", map[string]any{"grid": n.root, "add": []string{a.ID}}, &g)
	call(t, s, "select", map[string]any{"grid": well.ChildGridId, "add": []string{inner.ID}}, &g)
	f := mkText(t, s, n.feedRoot, "f")
	call(t, s, "select", map[string]any{"grid": n.feedRoot, "add": []string{f.ID}}, &g)
	refusedWith(t, callRaw(t, s, "select", map[string]any{"grid": n.root, "clear": true, "all": true}), "refused:")
	call(t, s, "select", map[string]any{"clear": true, "all": true}, &g)
	for _, id := range []string{n.root, well.ChildGridId, n.feedRoot} {
		call(t, s, "read_grid", map[string]any{"id": id}, &g)
		if len(g.Grid.Selection) != 0 {
			t.Fatalf("after the all-clear grid %s holds %v", id, g.Grid.Selection)
		}
	}
}
