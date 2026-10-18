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
	"github.com/josephburnett/gridwell/internal/mcpapi"
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
	s, err := (&mcpNode{url: hs.URL + mcpapi.Path, web: hs.Client()}).agent(t, h)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// listed is the sessions answer, read until want holds or three seconds
// pass: a stream is counted once the node is listening.
func listed(t *testing.T, s *mcp.ClientSession, want func(mcpapi.SessionsOut) bool) mcpapi.SessionsOut {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var out mcpapi.SessionsOut
		call(t, s, "sessions", nil, &out)
		if want(out) || time.Now().After(deadline) {
			return out
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func sessionIDs(out mcpapi.SessionsOut) []string {
	var ids []string
	for _, x := range out.Sessions {
		ids = append(ids, x.ID)
	}
	return ids
}

// sessions lists every screen showing the node, the newest change first, each
// with its panes' places, and leaves out another node telling its share,
// which shows no screen. No screen is an empty list.
func TestMCPSessionsListsEveryScreenWithItsPanes(t *testing.T) {
	f := newShellAPIFixture(t, Config{})
	s := agentAt(t, f.hs, nil)
	var out mcpapi.SessionsOut
	call(t, s, "sessions", nil, &out)
	if out.Sessions == nil || len(out.Sessions) != 0 {
		t.Fatalf("with no screen, sessions = %+v, want an empty list", out)
	}

	desk := viewer(t, f.hs, "desktop", focusedOn(f.root),
		&gridwellv1.ViewPane{Id: "p2", Anchor: f.root, Path: []string{"k1"}, ContentId: "c1", GridId: f.root})
	far := viewer(t, f.hs, "node")
	if err := far.SetView(context.Background(), &gridwellv1.SetViewRequest{Label: "node", ShownGridIds: []string{f.root}}); err != nil {
		t.Fatal(err)
	}
	phone := viewer(t, f.hs, "browser", focusedOn(f.root))

	out = listed(t, s, func(o mcpapi.SessionsOut) bool { return len(o.Sessions) == 2 })
	if got := sessionIDs(out); !slices.Equal(got, []string{phone.Session(), desk.Session()}) {
		t.Fatalf("sessions = %v, want the browser %s then the desktop %s, not the node's", got, phone.Session(), desk.Session())
	}
	d := out.Sessions[1]
	if d.Label != "desktop" || d.Changed == "" || d.AwaySince != "" || d.Yours || len(d.Panes) != 2 {
		t.Fatalf("desktop = %+v, want open, labelled, with two panes", d)
	}
	if q := d.Panes[0]; !q.Focused || q.Grid != f.root || q.Anchor != f.root {
		t.Errorf("pane 1 = %+v, want focused on %s", q, f.root)
	}
	if q := d.Panes[1]; q.Focused || q.Content != "c1" || !slices.Equal(q.Path, []string{"k1"}) {
		t.Errorf("pane 2 = %+v, want inside c1 by path [k1], unfocused", q)
	}

	// Saying its view again moves the desktop to the front.
	say(t, desk, "desktop", focusedOn(f.root), &gridwellv1.ViewPane{Id: "p2", Anchor: f.root, GridId: f.root})
	call(t, s, "sessions", nil, &out)
	if got := sessionIDs(out); !slices.Equal(got, []string{desk.Session(), phone.Session()}) {
		t.Errorf("after the desktop's view changed, sessions = %v, want it first", got)
	}
}

// paneTileView is a desktop session inside a pane tile whose layout holds two
// panes on root, the second focused. The layout is written and the view said
// as the client does both, through pane.EncodeLayout and pane.View.
func paneTileView(t *testing.T, f *shellAPIFixture) (*rpc.Client, *gridwellv1.Tile) {
	t.Helper()
	ctx := context.Background()
	pt, err := f.cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: f.root, Tile: &gridwellv1.Tile{Kind: rpc.KindPane, X: 5, Y: 5, W: 1, H: 1, AltText: "work"}})
	if err != nil {
		t.Fatal(err)
	}
	tr := pane.TreeAtPlace("w1:", f.root, nil, rpc.ViewOf(0, 0, 1))
	second, err := tr.Split(pane.Vertical)
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.SetFocus(second.ID); err != nil {
		t.Fatal(err)
	}
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

// Inside a pane tile the session names the pane tile, and the selection the
// panes show is the grid row's: a select writes the row and read_grid answers
// it (2026-10-18).
func TestMCPSessionsInsideAPaneTileAndItsGridsSelection(t *testing.T) {
	f := newShellAPIFixture(t, Config{})
	s := agentAt(t, f.hs, nil)
	mkText(t, s, f.root, "a")
	b := mkText(t, s, f.root, "b")
	cl, pt := paneTileView(t, f)

	out := listed(t, s, func(o mcpapi.SessionsOut) bool { return len(o.Sessions) == 1 })
	if len(out.Sessions) != 1 || out.Sessions[0].ID != cl.Session() || !slices.Equal(out.Sessions[0].PaneTiles, []string{pt.Id}) ||
		len(out.Sessions[0].Panes) != 2 {
		t.Fatalf("sessions = %+v, want %s inside pane tile %s with its two panes", out, cl.Session(), pt.Id)
	}
	var g mcpapi.GridOut
	call(t, s, "select", map[string]any{"grid": f.root, "add": []string{b.ID}}, &g)
	call(t, s, "read_grid", map[string]any{"id": f.root}, &g)
	if !slices.Equal(g.Grid.Selection, []string{b.ID}) {
		t.Fatalf("select inside a pane tile left the row at %v, want [%s]", g.Grid.Selection, b.ID)
	}
}

// A screen whose stream closed, as a phone's does in the background, is still
// listed, marked with when it went away.
func TestMCPSessionsListsAnAwayScreen(t *testing.T) {
	f := newShellAPIFixture(t, Config{})
	phone := rpc.NewClient(f.hs.Client(), f.hs.URL, connect.WithProtoJSON())
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := phone.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	say(t, phone, "browser", focusedOn(f.root))
	s := agentAt(t, f.hs, nil)
	out := listed(t, s, func(o mcpapi.SessionsOut) bool { return len(o.Sessions) == 1 })
	if len(out.Sessions) != 1 || out.Sessions[0].ID != phone.Session() || out.Sessions[0].AwaySince != "" {
		t.Fatalf("sessions = %+v, want the open browser %s", out, phone.Session())
	}

	cancel()
	_ = stream.Close()
	out = listed(t, s, func(o mcpapi.SessionsOut) bool { return len(o.Sessions) == 1 && o.Sessions[0].AwaySince != "" })
	if len(out.Sessions) != 1 || out.Sessions[0].ID != phone.Session() || out.Sessions[0].AwaySince == "" {
		t.Fatalf("after the browser's stream closed, sessions = %+v, want it away", out)
	}
}

// attach goes live on a shell tile from cl's session, as the wasm client's
// shell stack does.
func attach(t *testing.T, f *shellAPIFixture, cl *rpc.Client, tile string) {
	t.Helper()
	reg := shellstream.New(shellws.Dialer(shellws.Options{Origin: f.hs.URL, HTTPClient: f.hs.Client(), Session: cl.Session()}),
		func(string, []byte) {}, func(shellstream.Exit) {})
	reg.Open(tile, tile, 80, 24)
	t.Cleanup(func() { reg.Close(tile) })
}

// The X-Gridwell-Shell header names the agent's shell, and sessions marks
// yours the screen holding it live now, the newest attachment after a
// takeover. A shell live nowhere marks none and says so.
func TestMCPSessionsMarksTheScreenHoldingYourShell(t *testing.T) {
	f := newShellAPIFixture(t, Config{})
	tile := f.createShell(t, 0, 0)
	desk := viewer(t, f.hs, "desktop", focusedOn(f.root))
	attach(t, f, desk, tile.Id)
	waitSession(t, f.fake)
	phone := viewer(t, f.hs, "browser", focusedOn(f.root))

	key := rpc.LocalOf(rpc.ShellSession(tile))
	s := agentAt(t, f.hs, map[string]string{mcpapi.ShellHeader: key})
	yours := func(o mcpapi.SessionsOut) string {
		for _, x := range o.Sessions {
			if x.Yours {
				return x.ID
			}
		}
		return ""
	}
	if out := listed(t, s, func(o mcpapi.SessionsOut) bool { return yours(o) == desk.Session() }); yours(out) != desk.Session() {
		t.Fatalf("sessions = %+v, want %s, whose pane holds shell %s, marked yours", out, desk.Session(), key)
	}

	attach(t, f, phone, tile.Id)
	if out := listed(t, s, func(o mcpapi.SessionsOut) bool { return yours(o) == phone.Session() }); yours(out) != phone.Session() {
		t.Fatalf("after the browser took the shell over, sessions = %+v, want %s yours", out, phone.Session())
	}

	var out mcpapi.SessionsOut
	call(t, agentAt(t, f.hs, map[string]string{mcpapi.ShellHeader: "zzzzzzz"}), "sessions", nil, &out)
	if yours(out) != "" || !strings.Contains(out.Note, "zzzzzzz") || len(out.Sessions) != 2 {
		t.Fatalf("with a shell live nowhere, sessions = %+v, want none yours and a note naming the shell", out)
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
	var g mcpapi.GridOut
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

// read_tiles on a pane tile answers its layout as the panes it holds: each
// one's place, the grid the blob names, its content tile and whether it is
// the focused one, with the version the layout was read at; a link to the
// pane tile answers the same.
func TestMCPReadTilesOnAPaneTileAnswersItsLayout(t *testing.T) {
	f := newShellAPIFixture(t, Config{})
	s := agentAt(t, f.hs, nil)
	ctx := context.Background()
	well, err := f.cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: f.root, Tile: &gridwellv1.Tile{Kind: rpc.KindWell, X: 3, Y: 3, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	note := mkText(t, s, well.ChildGridId, "note")
	pt, err := f.cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: f.root, Tile: &gridwellv1.Tile{Kind: rpc.KindPane, X: 5, Y: 5, W: 1, H: 1, AltText: "work"}})
	if err != nil {
		t.Fatal(err)
	}
	tr := pane.TreeAtPlace("", f.root, nil, rpc.ViewOf(0, 0, 1))
	first := tr.FocusedPane()
	second, err := tr.Split(pane.Vertical)
	if err != nil {
		t.Fatal(err)
	}
	second.Push(pane.Frame{Door: well.Id})
	second.Push(pane.Frame{Door: note.ID, Content: true})
	if err := tr.SetFocus(second.ID); err != nil {
		t.Fatal(err)
	}
	data, _, err := pane.EncodeLayout(tr, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.cl.WriteContent(ctx, pt.Id, rpc.ContentBasis{Version: pt.Version}, data); err != nil {
		t.Fatal(err)
	}
	link := landed(t, items(t, s, "copy", map[string]any{"id": pt.Id, "link": true})[0])

	for _, r := range readTiles(t, s, pt.Id, link.ID) {
		l := r.Layout
		if r.Verdict != "ok" || l == nil || l.Version != pt.Version+1 || len(l.Panes) != 2 {
			t.Fatalf("read_tiles = %+v (layout %+v), want two panes at version %d", r, l, pt.Version+1)
		}
		p1, p2 := l.Panes[0], l.Panes[1]
		if p1.ID != first.ID || p1.Anchor != f.root || len(p1.Path) != 0 || p1.Grid != f.root || p1.Content != "" || p1.Focused {
			t.Errorf("pane 1 = %+v, want unfocused on %s", p1, f.root)
		}
		if p2.ID != second.ID || p2.Anchor != f.root || !slices.Equal(p2.Path, []string{well.Id}) || p2.Grid != "" ||
			p2.Content != note.ID || !p2.Focused {
			t.Errorf("pane 2 = %+v, want focused inside %s through %s, its grid not named", p2, note.ID, well.Id)
		}
	}
}
