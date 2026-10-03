package server

// The hey plugin through the whole shipped stack: the binary spawned by the
// production loader, the pluginhost adapter, the router, and the /content/
// door as a browser reaches it.
//
// Three things can only be wrong here: the adapter turns each declared context
// into a grid id the node can serve, so a menu entry with no grid id opens
// nothing; every collection lists through that mapping; and an email is a url
// tile carrying serves_page in everything, which every box links to, so the
// plugin never sees a URL and the door never sees the email.
//
// Nothing is injected. The plugin lives in another repository, so the
// subprocess is its only door, and testdata/fake-hey is the CLI's contract as
// an executable handed over as the `binary` config key, so the run, the argv,
// the environment and the JSON parse are all real.

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// heyNS is the registry key for the hey plugin in these tests.
const heyNS = "uh1"

// tileWithLabel finds a tile whose banner holds want. A mail tile's label is
// the plugin's own composition — marks, sender, subject — so a seam test names
// the part it is about rather than the whole string. Both mail seam tests use
// it.
func tileWithLabel(t *testing.T, grid *gridwellv1.GetGridResponse, want string) *gridwellv1.Tile {
	t.Helper()
	for _, tl := range grid.Tiles {
		if strings.Contains(tl.AltText, want) {
			return tl
		}
	}
	t.Fatalf("no tile labelled %q in %v", want, grid.Tiles)
	return nil
}

// heyStack spawns the plugin over the stand-in CLI and stands it up behind the
// real browser door, answering with the node-facing Info the adapter derived.
func heyStack(t *testing.T) (*httptest.Server, namespace.Namespace, *gridwellv1.InfoResponse) {
	t.Helper()
	// An absolute path: the plugin is a subprocess and nothing promises it the
	// test's working directory.
	bin, err := filepath.Abs(filepath.Join("testdata", "fake-hey"))
	if err != nil {
		t.Fatal(err)
	}
	cl := newPluginClient(t, "hey", map[string]string{"binary": bin})
	reg := plugin.NewRegistry()
	reg.Register(heyNS, "hey", cl, nil)
	hs := serveWeb(t, mustNew(t, reg, Config{}))

	info, err := cl.Info(t.Context(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	return hs, cl, info
}

// The plugin's four doorways are four (+) menu entries — imbox, reply later,
// set aside and everything, no landing grid, no privileged one — and every one
// of them lists its own emails. The mapping from a declared context to a
// servable grid id is the adapter's, so a plugin unit test cannot see it: it
// would find the entries declared and never learn whether any of them opens
// anything.
func TestHeyPluginDeclaresAndListsEveryCollection(t *testing.T) {
	_, cl, info := heyStack(t)
	ctx := t.Context()

	if info.RootGridId != "" {
		t.Fatalf("a plugin is not a place; it named a grid of its own: %q", info.RootGridId)
	}
	want := []string{"imbox", "reply later", "set aside", "everything"}
	if len(info.MenuEntries) != len(want) {
		t.Fatalf("menu entries = %v, want %q", info.MenuEntries, want)
	}
	for i, m := range info.MenuEntries {
		if m.GridId == "" {
			t.Errorf("menu entry %q opens no grid", m.Label)
		}
		if m.Label != want[i] {
			t.Errorf("menu entry %d = %q, want %q", i, m.Label, want[i])
		}
	}

	// Every collection lists, each through its mapped id.
	for _, c := range []struct{ grid, holds string }{
		{info.MenuEntries[0].GridId, "Lunch plans"},
		{info.MenuEntries[1].GridId, "Conference talk"},
		{info.MenuEntries[2].GridId, "Lease renewal"},
		// The Feed is no doorway, but it is walked into everything.
		{info.MenuEntries[3].GridId, "Weekly digest"},
	} {
		g, err := cl.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: c.grid})
		if err != nil {
			t.Fatalf("GetGrid %s: %v", c.grid, err)
		}
		tileWithLabel(t, g, c.holds)
	}

	// Every email arrives in everything as a url tile whose page the plugin
	// serves. That is the shape the whole client rests on: the address is the
	// node's to derive at its /content/ door, so the email declares none of
	// its own, and a text tile could not serve one at all.
	all, err := cl.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: info.MenuEntries[3].GridId})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Tiles) != 5 {
		t.Fatalf("everything = %v, want its five threads", all.Tiles)
	}
	for _, tl := range all.Tiles {
		if tl.Kind != rpc.KindURL || !tl.ServesPage || tl.UrlString != "" || tl.LinkTargetId != "" {
			t.Errorf("%s = kind %q serves_page %v url %q link %q", tl.AltText, tl.Kind, tl.ServesPage, tl.UrlString, tl.LinkTargetId)
		}
	}
	// A box holds links to those tiles, each with the box's own face.
	root, err := cl.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: info.MenuEntries[0].GridId})
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Tiles) != 2 {
		t.Fatalf("the Imbox = %v, want its two threads", root.Tiles)
	}
	for _, tl := range root.Tiles {
		if tl.LinkTargetId == "" {
			t.Errorf("%s in the Imbox is no link", tl.AltText)
		}
	}
	// And the unread mark rides the label, so the two threads read differently.
	unseen := tileWithLabel(t, root, "Lunch plans")
	seen := tileWithLabel(t, root, "Invoice 41")
	if unseen.StatusDetail != "unseen" || seen.StatusDetail != "seen" {
		t.Errorf("status = %q, %q", unseen.StatusDetail, seen.StatusDetail)
	}
}

// Descending into an email opens the email itself, as HEY's own HTML, through
// the door a browser reaches, whether the tile is the thread in everything or
// a box's link to it. Neither side of the seam can check this: the plugin
// answers a key and never sees the URL, and the door addresses a tile and
// never sees the email.
func TestHeyPluginServesAnEmailThroughTheContentDoor(t *testing.T) {
	hs, cl, info := heyStack(t)
	for _, door := range []struct{ name, grid string }{
		{"a box's link", info.MenuEntries[0].GridId},
		{"everything's tile", info.MenuEntries[3].GridId},
	} {
		g, err := cl.GetGrid(t.Context(), &gridwellv1.GetGridRequest{GridId: door.grid})
		if err != nil {
			t.Fatal(err)
		}
		email := tileWithLabel(t, g, "Lunch plans")

		res, body := get(t, hs.Client(), rpc.PageURL(hs.URL, ContentToken(testPassword), heyNS+"/"+email.Id), "")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET the email through %s = %d %q", door.name, res.StatusCode, body)
		}
		if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("%s: Content-Type = %q", door.name, ct)
		}
		if !strings.Contains(body, "Are you free friday?") {
			t.Errorf("%s: the email did not arrive: %.200q", door.name, body)
		}
		// The door's invariant rides an email like any other page.
		if csp := res.Header.Get("Content-Security-Policy"); csp != "sandbox allow-scripts" {
			t.Errorf("%s: CSP = %q", door.name, csp)
		}
	}
}
