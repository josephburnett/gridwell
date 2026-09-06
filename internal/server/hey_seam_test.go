package server

// The hey plugin through the whole shipped stack: the binary spawned by the
// production loader, the pluginhost adapter, the router, and the /content/
// door as a browser reaches it.
//
// The seam is what is under test, not the plugin's insides. Three things can
// only be wrong here. The plugin declares one landing grid and names its other
// collections on the (+) menu — the adapter turns each declared context into a
// grid id the node can serve, and a menu entry with no grid id is an entry
// that opens nothing. Every collection has to list through that mapping, not
// just the root. And an email is a TEXT tile carrying serves_page, whose page
// the node addresses and the door serves: the plugin never sees a URL and the
// door never sees the email.
//
// Nothing is injected: the plugin lives in another repository and the
// subprocess is its only door. HEY is not injected either — testdata/fake-hey
// is the CLI's contract as an executable, handed over as the plugin's `binary`
// config key, so the run, the argv, the environment and the JSON parse are all
// real. The two repositories share that contract, never a package.

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

// The plugin's landing grid is the Imbox and its other two collections ride
// the (+) menu — and every one of the three lists its own emails. The mapping
// from a declared context to a servable grid id is the adapter's, so a plugin
// unit test cannot see it: it would find the entries declared and never learn
// whether any of them opens anything.
func TestHeyPluginDeclaresAndListsEveryCollection(t *testing.T) {
	_, cl, info := heyStack(t)
	ctx := t.Context()

	if info.RootGridId == "" {
		t.Fatal("the hey plugin declared no landing grid; its row on the (+) menu would enter nothing")
	}
	if len(info.MenuEntries) != 2 {
		t.Fatalf("menu entries = %v, want reply later and set aside", info.MenuEntries)
	}
	// The root is already the plugin's own row on that menu, so no entry may
	// name it again.
	for _, m := range info.MenuEntries {
		if m.GridId == "" {
			t.Errorf("menu entry %q opens no grid", m.Label)
		}
		if m.GridId == info.RootGridId {
			t.Errorf("menu entry %q opens the landing grid, which is already the plugin's row", m.Label)
		}
	}
	if info.MenuEntries[0].Label != "reply later" || info.MenuEntries[1].Label != "set aside" {
		t.Errorf("menu labels = %q, %q", info.MenuEntries[0].Label, info.MenuEntries[1].Label)
	}

	// Every collection lists, the two menu ones through their mapped ids.
	for _, c := range []struct{ grid, holds string }{
		{info.RootGridId, "Lunch plans"},
		{info.MenuEntries[0].GridId, "Conference talk"},
		{info.MenuEntries[1].GridId, "Lease renewal"},
	} {
		g, err := cl.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: c.grid})
		if err != nil {
			t.Fatalf("GetGrid %s: %v", c.grid, err)
		}
		tileWithLabel(t, g, c.holds)
	}

	// Every email arrives as a text tile that serves a page. That is the shape
	// the whole client rests on: a url tile's own address wins over
	// serves_page, so an email declared kind "url" would hand the node an
	// address it does not have and the email would never be served.
	root, err := cl.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: info.RootGridId})
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Tiles) != 2 {
		t.Fatalf("the Imbox = %v, want its two threads", root.Tiles)
	}
	for _, tl := range root.Tiles {
		if tl.Kind != rpc.KindText || !tl.ServesPage || tl.UrlString != "" {
			t.Errorf("%s = kind %q serves_page %v url %q", tl.AltText, tl.Kind, tl.ServesPage, tl.UrlString)
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
// the door a browser reaches. Neither side of the seam can check this: the
// plugin answers a key and never sees the URL, and the door addresses a tile
// and never sees the email.
func TestHeyPluginServesAnEmailThroughTheContentDoor(t *testing.T) {
	hs, cl, info := heyStack(t)
	root, err := cl.GetGrid(t.Context(), &gridwellv1.GetGridRequest{GridId: info.RootGridId})
	if err != nil {
		t.Fatal(err)
	}
	email := tileWithLabel(t, root, "Lunch plans")

	res, body := get(t, hs.Client(), rpc.PageURL(hs.URL, ContentToken(testPassword), heyNS+"/"+email.Id), "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET the email = %d %q", res.StatusCode, body)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
	if !strings.Contains(body, "Are you free friday?") {
		t.Errorf("the email did not arrive: %.200q", body)
	}
	// The door's invariant rides an email like any other page.
	if csp := res.Header.Get("Content-Security-Policy"); csp != "sandbox allow-scripts" {
		t.Errorf("CSP = %q", csp)
	}
}
