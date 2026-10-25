package rpc

import (
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

func TestIsDoorKind(t *testing.T) {
	if !IsDoorKind(KindDoor) {
		t.Errorf("IsDoorKind(%q) = false, want true", KindDoor)
	}
	notDoors := []string{KindText, KindURL, KindShell, ""}
	for _, k := range notDoors {
		if IsDoorKind(k) {
			t.Errorf("IsDoorKind(%q) = true, want false", k)
		}
	}
}

// TestIsContentDescentKind pins the content-descent set the click router and
// the URL-restore walk both rely on. Shell must be included: it sets
// TextFocus and is encoded into the URL, so it has to round-trip on reload.
// The door kinds must not be, because they are grid descents.
func TestIsContentDescentKind(t *testing.T) {
	content := []string{KindText, KindURL, KindShell}
	for _, k := range content {
		if !IsContentDescentKind(k) {
			t.Errorf("IsContentDescentKind(%q) = false, want true", k)
		}
	}
	notContent := []string{KindDoor, ""}
	for _, k := range notContent {
		if IsContentDescentKind(k) {
			t.Errorf("IsContentDescentKind(%q) = true, want false", k)
		}
	}
}

func TestQualifyIDUUIDOfRoundTrip(t *testing.T) {
	cases := []struct{ uuid, local string }{
		{"abc-uuid", "42"},
		{"u", "1"},
	}
	for _, c := range cases {
		id := QualifyID(c.uuid, c.local)
		if want := c.uuid + "/" + c.local; id != want {
			t.Errorf("QualifyID(%q,%q) = %q, want %q", c.uuid, c.local, id, want)
		}
		if got := UUIDOf(id); got != c.uuid {
			t.Errorf("UUIDOf(%q) = %q, want %q", id, got, c.uuid)
		}
	}
	if got := UUIDOf("42"); got != "" {
		t.Errorf("UUIDOf(bare) = %q, want \"\"", got)
	}
	if got := UUIDOf(""); got != "" {
		t.Errorf("UUIDOf(empty) = %q, want \"\"", got)
	}
}

// TestSplitID pins the one-hop routing peel every layer shares. These cases
// are the contract: a chain peels exactly one segment, and a bare id, an
// empty id, and a degenerate leading "/" are all unqualified (ok=false),
// never a half-parse.
func TestSplitID(t *testing.T) {
	cases := []struct {
		id, uuid, rest string
		ok             bool
	}{
		{"u/1", "u", "1", true},
		{"ssh1/rp1/7", "ssh1", "rp1/7", true}, // a chain peels one segment
		{"u/", "u", "", true},                 // empty rest is still qualified
		{"42", "", "", false},                 // bare local id
		{"", "", "", false},
		{"/x", "", "", false}, // degenerate: no first segment
		{"/", "", "", false},
	}
	for _, c := range cases {
		uuid, rest, ok := SplitID(c.id)
		if uuid != c.uuid || rest != c.rest || ok != c.ok {
			t.Errorf("SplitID(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.id, uuid, rest, ok, c.uuid, c.rest, c.ok)
		}
		// UUIDOf is defined on SplitID; assert the agreement anyway so a
		// future re-implementation of either cannot drift silently.
		if got := UUIDOf(c.id); got != c.uuid {
			t.Errorf("UUIDOf(%q) = %q, disagrees with SplitID's uuid %q", c.id, got, c.uuid)
		}
	}
}

// TestLocalOf pins the display half of the codec. The invariant that makes
// LocalOf safe everywhere: QualifyID(NamespaceOf(id), LocalOf(id))
// reproduces any qualified id.
func TestLocalOf(t *testing.T) {
	cases := []struct{ id, local string }{
		{"u/7", "7"},
		{"ssh1/rp1/7", "7"},
		{"42", "42"}, // a bare id is its own local id
		{"", ""},
		{"u/", ""},
	}
	for _, c := range cases {
		if got := LocalOf(c.id); got != c.local {
			t.Errorf("LocalOf(%q) = %q, want %q", c.id, got, c.local)
		}
	}
	for _, id := range []string{"u/7", "ssh1/rp1/7", "a/b/c/d"} {
		if got := QualifyID(NamespaceOf(id), LocalOf(id)); got != id {
			t.Errorf("QualifyID(NamespaceOf, LocalOf) round trip of %q = %q", id, got)
		}
	}
}

func TestIsDoorToOtherNamespace(t *testing.T) {
	cases := []struct {
		name string
		tile *pb.Tile
		want bool
	}{
		{"interior door (same plugin) is not a door to another namespace",
			&pb.Tile{Kind: KindDoor, GridId: "u/1", ChildGridId: "u/2"}, false},
		{"cross-plugin door is a door to another namespace",
			&pb.Tile{Kind: KindDoor, GridId: "u/1", ChildGridId: "v/2"}, true},
		{"non-door is never a door to another namespace",
			&pb.Tile{Kind: KindText, GridId: "u/1", ChildGridId: "v/2"}, false},
		{"door with no child grid is not a door to another namespace",
			&pb.Tile{Kind: KindDoor, GridId: "u/1"}, false},
		{"synthetic node, both ids empty, is not a door to another namespace",
			&pb.Tile{Kind: KindDoor}, false},
		// A menu swatch's exact shape: no owning grid, qualified child grid.
		{"synthetic door node (empty grid_id, qualified child_grid_id) is a door to another namespace",
			&pb.Tile{Kind: KindDoor, ChildGridId: "plugin-uuid/1"}, true},
		{"home door onto a far home's grid, one hop, is a door to another namespace",
			&pb.Tile{Kind: KindDoor, GridId: "n1/3", ChildGridId: "n1/geneva/n2/5"}, true},
		{"far door onto a grid two hops further is a door to another namespace",
			&pb.Tile{Kind: KindDoor, GridId: "n1/geneva/n2/3", ChildGridId: "n1/geneva/n2/paris/n3/5"}, true},
		{"far door onto the far node's own plugin grid is a door to another namespace",
			&pb.Tile{Kind: KindDoor, GridId: "n1/geneva/n2/3", ChildGridId: "n1/geneva/fs/~L2hvbWU"}, true},
		{"far link door onto another grid of the far home is not a door to another namespace",
			&pb.Tile{Kind: KindDoor, Link: true, GridId: "n1/geneva/n2/3", ChildGridId: "n1/geneva/n2/8"}, false},
		{"home link door onto another home grid is not a door to another namespace",
			&pb.Tile{Kind: KindDoor, Link: true, GridId: "n1/3", ChildGridId: "n1/8"}, false},
	}
	for _, c := range cases {
		if got := IsDoorToOtherNamespace(c.tile); got != c.want {
			t.Errorf("%s: IsDoorToOtherNamespace = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPluginDoorTile(t *testing.T) {
	pl := &pb.PluginInfo{Label: "files", RootGridId: "fs-uuid/1",
		RootViewCx: 3, RootViewCy: -2, RootViewZoom: 0.5}
	got := PluginDoorTile(pl)
	// The load-bearing invariants the swatch preview depends on: the tile is
	// a door carrying the plugin's root grid as its child, which makes it an
	// door to another namespace, so the client fetches and previews that grid.
	if !IsDoorKind(got.Kind) {
		t.Errorf("PluginDoorTile kind = %q, want a door", got.Kind)
	}
	if got.ChildGridId != pl.RootGridId {
		t.Errorf("PluginDoorTile ChildGridID = %q, want %q", got.ChildGridId, pl.RootGridId)
	}
	if !IsDoorToOtherNamespace(got) {
		t.Errorf("PluginDoorTile is not a door to another namespace; a door would draw an inert interior door")
	}
	// Link is the one "this is a link" signal, and the client reads it
	// alone. This synthetic tile never passes through the server's
	// qualifyTiles, so it must stamp the bit itself or the menu swatch loses
	// its dashed border.
	if !got.Link {
		t.Error("PluginDoorTile does not set Link; the door swatch would render as owned content, not a link")
	}
	if got.AltText != pl.Label {
		t.Errorf("PluginDoorTile AltText = %q, want %q", got.AltText, pl.Label)
	}
	// The plugin's persisted root framing rides as the tile's framing. It is
	// one shape, so it carries across verbatim, and a + menu descent lands at
	// the left-off view.
	if got.ViewCx != 3 || got.ViewCy != -2 || got.ViewZoom != 0.5 {
		t.Errorf("PluginDoorTile framing = (%v,%v,%v), want the plugin root framing (3,-2,0.5)",
			got.ViewCx, got.ViewCy, got.ViewZoom)
	}
}

// TestKindPartition pins the three descent-class predicates as a partition
// of the descendable kinds: every kind belongs to exactly one of grid
// descent (IsDoorKind), content descent (IsContentDescentKind), and workspace tile
// descent (IsWorkspaceKind). A new kind that joins no class, or two, falls
// through or double-fires the click router and the URL-restore walk.
func TestKindPartition(t *testing.T) {
	all := []string{KindDoor, KindText, KindURL, KindShell, KindWorkspace}
	for _, k := range all {
		n := 0
		if IsDoorKind(k) {
			n++
		}
		if IsContentDescentKind(k) {
			n++
		}
		if IsWorkspaceKind(k) {
			n++
		}
		if n != 1 {
			t.Errorf("kind %q belongs to %d descent classes, want exactly 1", k, n)
		}
	}
	if !IsWorkspaceKind(KindWorkspace) {
		t.Errorf("IsWorkspaceKind(%q) = false, want true", KindWorkspace)
	}
	for _, k := range []string{KindDoor, KindText, KindURL, KindShell, ""} {
		if IsWorkspaceKind(k) {
			t.Errorf("IsWorkspaceKind(%q) = true, want false", k)
		}
	}
}

// TestHomeGrid: "/" means the handshake's home_grid_id and never a row's
// position, so a rooted first row is not a home. It is the one boot, URL and
// connection landing derivation.
func TestHomeGrid(t *testing.T) {
	first := &pb.PluginInfo{Uuid: "p1", RootGridId: "p1/1"}
	if got := HomeGrid(&pb.HandshakeResponse{HomeGridId: "n/1", Plugins: []*pb.PluginInfo{first}}); got != "n/1" {
		t.Errorf("HomeGrid = %q, want the handshake's home_grid_id", got)
	}
	if got := HomeGrid(&pb.HandshakeResponse{Plugins: []*pb.PluginInfo{first}}); got != "" {
		t.Errorf("HomeGrid = %q, want \"\": a rooted row is not a home", got)
	}
	if got := HomeGrid(nil); got != "" {
		t.Errorf("HomeGrid(nil) = %q, want empty", got)
	}
}

// TestHomeRow: the home's framing is read from the row rooted at
// home_grid_id, never from a row's position or a copy on the handshake.
func TestHomeRow(t *testing.T) {
	other := &pb.PluginInfo{Uuid: "p", RootGridId: "n/2", RootViewZoom: 9}
	home := &pb.PluginInfo{Uuid: "n", RootGridId: "n/1", RootViewCx: 3, RootViewCy: 4, RootViewZoom: 2}
	l := &pb.HandshakeResponse{HomeGridId: "n/1", Plugins: []*pb.PluginInfo{other, home}}
	if got := HomeRow(l); got != home {
		t.Errorf("HomeRow = %v, want the row rooted at home_grid_id", got)
	}
	if got := HomeRow(&pb.HandshakeResponse{Plugins: []*pb.PluginInfo{other}}); got != nil {
		t.Errorf("HomeRow = %v, want nil: no home_grid_id names no row", got)
	}
	if got := HomeRow(&pb.HandshakeResponse{HomeGridId: "n/3", Plugins: []*pb.PluginInfo{other}}); got != nil {
		t.Errorf("HomeRow = %v, want nil: a home no row is rooted at", got)
	}
}

// TestContentID: the one resolution point for read-through. A leaf link's
// content operations key by its target; an owned tile keys by itself. Every
// client content path (body fetch, edit buffer, save routing, preview fetch,
// shell's tmux, pane layout) reads this, so a link and its target share one
// content fact by construction.
func TestContentID(t *testing.T) {
	link := &pb.Tile{Id: "b/9", Kind: KindText, LinkTargetId: "a/42"}
	if got := ContentID(link); got != "a/42" {
		t.Errorf("link ContentID = %q, want the target a/42", got)
	}
	owned := &pb.Tile{Id: "a/42", Kind: KindText}
	if got := ContentID(owned); got != "a/42" {
		t.Errorf("owned ContentID = %q, want its own id", got)
	}
}

// A link names content, never a middleman: a link to a link targets what the
// first one targets, and a door's link is a door to another namespace onto the same grid,
// framed where the source was.
func TestLinkOfNamesTheContentNotTheMiddleman(t *testing.T) {
	link := LinkOf(&pb.Tile{Id: "b/9", Kind: KindText, W: 2, H: 1, LinkTargetId: "a/42", AltText: "notes"}, 3, 4)
	if link.LinkTargetId != "a/42" || link.Kind != KindText || link.X != 3 || link.Y != 4 || link.W != 2 || link.AltText != "notes" {
		t.Errorf("leaf link = %v, want a 2x1 text link to a/42 at (3,4)", link)
	}
	door := LinkOf(&pb.Tile{Id: "a/7", Kind: KindDoor, W: 1, H: 1, ChildGridId: "a/8", ViewZoom: 2}, 0, 0)
	if door.ChildGridId != "a/8" || door.LinkTargetId != "" || door.ViewZoom != 2 {
		t.Errorf("door link = %v, want a door to another namespace onto a/8 keeping its framing", door)
	}
}

// TestTextDocumentAndPageContent pins the two derivations of "is this a
// page", which were spelled three ways across the shim before they had
// owners. A served page is a url tile: web content wherever the address comes
// from, and never a document. The last row is the shape acceptEntries refuses
// (pluginhost.acceptEntries), so no client meets it; read here it is the plain
// document its kind says it is.
func TestTextDocumentAndPageContent(t *testing.T) {
	cases := []struct {
		name                   string
		tile                   *pb.Tile
		document, page, webCon bool
	}{
		{"text document", &pb.Tile{Kind: KindText}, true, false, false},
		{"page tile", &pb.Tile{Kind: KindURL, ServesPage: true}, false, true, true},
		{"url tile at its own address", &pb.Tile{Kind: KindURL}, false, false, true},
		{"door", &pb.Tile{Kind: KindDoor}, false, false, false},
		{"text row flagged serves_page", &pb.Tile{Kind: KindText, ServesPage: true}, true, false, false},
	}
	for _, c := range cases {
		if got := TextDocument(c.tile); got != c.document {
			t.Errorf("%s: TextDocument = %v, want %v", c.name, got, c.document)
		}
		if got := PageContent(c.tile); got != c.page {
			t.Errorf("%s: PageContent = %v, want %v", c.name, got, c.page)
		}
		if got := WebContent(c.tile); got != c.webCon {
			t.Errorf("%s: WebContent = %v, want %v", c.name, got, c.webCon)
		}
	}
}

// TestLeafLink pins that the link question is the target's presence, not the
// Link flag: a door with a qualified child grid is a link too, and
// it owns its row.
func TestLeafLink(t *testing.T) {
	link := &pb.Tile{Id: "b/9", Kind: KindText, LinkTargetId: "a/42", Link: true}
	if !LeafLink(link) {
		t.Error("a row with a target is a leaf link")
	}
	farDoor := &pb.Tile{Id: "b/1", Kind: KindDoor, ChildGridId: "a/7", Link: true}
	if LeafLink(farDoor) {
		t.Error("a door to another namespace is not a leaf link")
	}
	owned := &pb.Tile{Id: "a/42", Kind: KindText}
	if LeafLink(owned) {
		t.Error("a row that owns its content is not a leaf link")
	}
}

// TestDescentOf pins the one classification the bar slot and the input layer
// both read. It is a partition of the kinds, so no tile is two families at
// once and no reader has an arm order to get wrong.
func TestDescentOf(t *testing.T) {
	cases := []struct {
		name string
		tile *pb.Tile
		want Descent
	}{
		{"url tile", &pb.Tile{Kind: KindURL}, DescentURL},
		{"page tile", &pb.Tile{Kind: KindURL, ServesPage: true}, DescentURL},
		{"shell tile", &pb.Tile{Kind: KindShell}, DescentShell},
		{"text document", &pb.Tile{Kind: KindText}, DescentNone},
		{"door", &pb.Tile{Kind: KindDoor}, DescentNone},
		{"unresolved", nil, DescentNone},
	}
	for _, c := range cases {
		if got := DescentOf(c.tile); got != c.want {
			t.Errorf("%s: DescentOf = %v, want %v", c.name, got, c.want)
		}
	}
}

// A root grid's framing lives on every door rooted at it, a row's own and a
// declared entry's alike, and on nothing else. Writing what is already there
// changes nothing, so the pane that made the write never sees its own echo.
func TestReframeWritesEveryDoorRootedAtTheGrid(t *testing.T) {
	menu := []*pb.PluginInfo{
		{Uuid: "loc", RootGridId: "loc/1",
			PluginGrids: []*pb.PluginGrid{{Id: "trash", GridId: "loc/9"}, {Id: "same", GridId: "loc/1"}}},
		{Uuid: "p", PluginGrids: []*pb.PluginGrid{{Id: "feed", GridId: "p/~ZmVlZA"}}},
	}
	f, _ := NewFraming(1.5, -2, 0.75)
	if !Reframe("loc/1", f, menu) {
		t.Fatal("a new framing reported no change")
	}
	if pl := menu[0]; pl.RootViewCx != 1.5 || pl.RootViewCy != -2 || pl.RootViewZoom != 0.75 {
		t.Errorf("the row rooted at the grid = %+v", pl)
	}
	if e := menu[0].PluginGrids[1]; e.ViewCx != 1.5 || e.ViewCy != -2 || e.ViewZoom != 0.75 {
		t.Errorf("the entry declaring the grid = %+v", e)
	}
	if e := menu[0].PluginGrids[0]; e.ViewZoom != 0 {
		t.Errorf("another grid's entry moved: %+v", e)
	}
	if Reframe("loc/1", f, menu) {
		t.Error("an equal framing reported a change")
	}
	if !Reframe("p/~ZmVlZA", f, menu) || menu[1].PluginGrids[0].ViewZoom != 0.75 {
		t.Errorf("a plugin grid's entry = %+v", menu[1].PluginGrids[0])
	}
	if Reframe("x/1", f, menu) || Reframe("", f, menu) {
		t.Error("a grid no door roots reported a change")
	}
}

// TestEveryKindHasARow: a kind with no row in the kind table can do nothing,
// so every site that asks it falls through every arm.
func TestEveryKindHasARow(t *testing.T) {
	for _, k := range FindKinds {
		if KindOf(k).Descends == DescendNone {
			t.Errorf("kind %q has no row in the kind table", k)
		}
	}
	if KindOf("") != (KindCaps{}) {
		t.Error("an unknown kind reads a row that can do something")
	}
}

// A presented tile's page and its site address convert both ways by one
// rule, and only a location inside the tile's page names a site address.
func TestPresentedPageURLAndBack(t *testing.T) {
	const page = "http://127.0.0.1:9/content/tok/n1/12/"
	for site, want := range map[string]string{
		"http://example.test/a?x=1#f":               page + "example.test/a?x=1#f",
		"https://gitlab.com/g/p/-/merge_requests/7": page + "gitlab.com/g/p/-/merge_requests/7",
		"example.test/a":                            "",
		"https://":                                  "",
	} {
		got := PresentedPageURL(page, site)
		if got != want {
			t.Errorf("PresentedPageURL(%q) = %q, want %q", site, got, want)
			continue
		}
		if back, ok := PresentedSite(page, site, got); want != "" && (!ok || back != site) {
			t.Errorf("PresentedSite(%q) = %q %v, want %q back", got, back, ok, site)
		}
	}
	const site = "https://example.test/a"
	for name, location := range map[string]string{
		"another tile's page":        "http://127.0.0.1:9/content/tok/n1/13/example.test/b",
		"a tile whose id it extends": "http://127.0.0.1:9/content/tok/n1/123/example.test/b",
		"the plugin's assets":        page + "_/app.js",
		"another origin":             "https://example.test/b",
		"the page itself":            page,
		"the page's query":           page + "?x=1",
	} {
		if got, ok := PresentedSite(page, site, location); ok {
			t.Errorf("%s: PresentedSite = %q, want no site address", name, got)
		}
	}
	if got, ok := PresentedSite(page, site, page+"example.test/b?y=2"); !ok || got != "https://example.test/b?y=2" {
		t.Errorf("an in-page navigation = %q %v, want it in the site's scheme", got, ok)
	}
}
