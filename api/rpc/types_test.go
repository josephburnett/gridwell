package rpc

import (
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

func TestIsWellKind(t *testing.T) {
	if !IsWellKind(KindWell) {
		t.Errorf("IsWellKind(%q) = false, want true", KindWell)
	}
	notWells := []string{KindText, KindURL, KindShell, ""}
	for _, k := range notWells {
		if IsWellKind(k) {
			t.Errorf("IsWellKind(%q) = true, want false", k)
		}
	}
}

// TestIsContentDescentKind pins the content-descent set the click router and
// the URL-restore walk both rely on. Shell must be included: it sets
// TextFocus and is encoded into the URL, so it has to round-trip on reload.
// The well kinds must not be, because they are grid descents.
func TestIsContentDescentKind(t *testing.T) {
	content := []string{KindText, KindURL, KindShell}
	for _, k := range content {
		if !IsContentDescentKind(k) {
			t.Errorf("IsContentDescentKind(%q) = false, want true", k)
		}
	}
	notContent := []string{KindWell, ""}
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

func TestIsExitWell(t *testing.T) {
	cases := []struct {
		name string
		tile *pb.Tile
		want bool
	}{
		{"interior well (same plugin) is not an exit well",
			&pb.Tile{Kind: KindWell, GridId: "u/1", ChildGridId: "u/2"}, false},
		{"cross-plugin well is an exit well",
			&pb.Tile{Kind: KindWell, GridId: "u/1", ChildGridId: "v/2"}, true},
		{"non-well is never an exit well",
			&pb.Tile{Kind: KindText, GridId: "u/1", ChildGridId: "v/2"}, false},
		{"well with no child grid is not an exit well",
			&pb.Tile{Kind: KindWell, GridId: "u/1"}, false},
		{"synthetic node, both ids empty, is not an exit well",
			&pb.Tile{Kind: KindWell}, false},
		// A menu swatch's exact shape: no owning grid, qualified child grid.
		{"synthetic launcher node (empty grid_id, qualified child_grid_id) is an exit well",
			&pb.Tile{Kind: KindWell, ChildGridId: "plugin-uuid/1"}, true},
	}
	for _, c := range cases {
		if got := IsExitWell(c.tile); got != c.want {
			t.Errorf("%s: IsExitWell = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPluginWellTile(t *testing.T) {
	pl := &pb.PluginInfo{Label: "files", RootGridId: "fs-uuid/1",
		RootViewCx: 3, RootViewCy: -2, RootViewZoom: 0.5}
	got := PluginWellTile(pl)
	// The load-bearing invariants the swatch preview depends on: the tile is
	// a well carrying the plugin's root grid as its child, which makes it an
	// exit well, so the client fetches and previews that grid.
	if !IsWellKind(got.Kind) {
		t.Errorf("PluginWellTile kind = %q, want a well", got.Kind)
	}
	if got.ChildGridId != pl.RootGridId {
		t.Errorf("PluginWellTile ChildGridID = %q, want %q", got.ChildGridId, pl.RootGridId)
	}
	if !IsExitWell(got) {
		t.Errorf("PluginWellTile is not an exit well; launcher would draw an inert interior well")
	}
	// Reference is the one "this is a link" signal, and the client reads it
	// alone. This synthetic tile never passes through the server's
	// qualifyTiles, so it must stamp the bit itself or the menu swatch loses
	// its dashed border.
	if !got.Reference {
		t.Error("PluginWellTile does not set Reference; the launcher swatch would render as owned content, not a link")
	}
	if got.AltText != pl.Label {
		t.Errorf("PluginWellTile AltText = %q, want %q", got.AltText, pl.Label)
	}
	// The plugin's persisted root framing rides as the tile's framing. It is
	// one shape, so it carries across verbatim, and a + menu descent lands at
	// the left-off view.
	if got.ViewCx != 3 || got.ViewCy != -2 || got.ViewZoom != 0.5 {
		t.Errorf("PluginWellTile framing = (%v,%v,%v), want the plugin root framing (3,-2,0.5)",
			got.ViewCx, got.ViewCy, got.ViewZoom)
	}
}

// TestKindPartition pins the three descent-class predicates as a partition
// of the descendable kinds: every kind belongs to exactly one of grid
// descent (IsWellKind), content descent (IsContentDescentKind), and pane-tile
// descent (IsWorkspaceKind). A new kind that joins no class, or two, falls
// through or double-fires the click router and the URL-restore walk.
func TestKindPartition(t *testing.T) {
	all := []string{KindWell, KindText, KindURL, KindShell, KindPane}
	for _, k := range all {
		n := 0
		if IsWellKind(k) {
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
	if !IsWorkspaceKind(KindPane) {
		t.Errorf("IsWorkspaceKind(%q) = false, want true", KindPane)
	}
	for _, k := range []string{KindWell, KindText, KindURL, KindShell, ""} {
		if IsWorkspaceKind(k) {
			t.Errorf("IsWorkspaceKind(%q) = true, want false", k)
		}
	}
}

// TestHomeGrid: "/" means the handshake's home_grid_id, falling back to the
// first rooted plugin row and skipping rootless ones. It is the one boot and
// URL home derivation.
func TestHomeGrid(t *testing.T) {
	first := &pb.PluginInfo{Uuid: "p1", RootGridId: "p1/1"}
	second := &pb.PluginInfo{Uuid: "p2", RootGridId: "p2/1"}
	rootless := &pb.PluginInfo{Uuid: "p0"} // no root_grid_id: broken or rootless
	if got := HomeGrid(&pb.HandshakeResponse{HomeGridId: "n/1", Plugins: []*pb.PluginInfo{first}}); got != "n/1" {
		t.Errorf("HomeGrid = %q, want the handshake's home_grid_id", got)
	}
	if got := HomeGrid(&pb.HandshakeResponse{Plugins: []*pb.PluginInfo{rootless, second}}); got != "p2/1" {
		t.Errorf("HomeGrid = %q, want p2/1 (the first rooted row, when the field is absent)", got)
	}
	if got := HomeGrid(&pb.HandshakeResponse{Plugins: []*pb.PluginInfo{rootless}}); got != "" {
		t.Errorf("HomeGrid = %q, want \"\" (nothing rooted)", got)
	}
	if got := HomeGrid(&pb.HandshakeResponse{}); got != "" {
		t.Errorf("HomeGrid(empty) = %q, want empty", got)
	}
}

// TestContentID: the one resolution point for read-through. A leaf link's
// content operations key by its target; an owned tile keys by itself. Every
// client content door (body fetch, edit buffer, save routing, preview fetch,
// shell session, pane layout) reads this, so a link and its target share one
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

// TestTextDocumentAndPageContent pins the two derivations of "is this a
// page", which were spelled three ways across the shim before they had
// owners. The last row is the shape only the wire permits: a url row that
// also carries serves_page. Its own address wins, so it is web content and a
// url tile, never a page tile and never a document.
func TestTextDocumentAndPageContent(t *testing.T) {
	cases := []struct {
		name                   string
		tile                   *pb.Tile
		document, page, webCon bool
	}{
		{"text document", &pb.Tile{Kind: KindText}, true, false, false},
		{"page tile", &pb.Tile{Kind: KindText, ServesPage: true}, false, true, true},
		{"url tile", &pb.Tile{Kind: KindURL}, false, false, true},
		{"well", &pb.Tile{Kind: KindWell}, false, false, false},
		{"url row flagged serves_page", &pb.Tile{Kind: KindURL, ServesPage: true}, false, false, true},
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
// Reference flag: a well with a qualified child grid is a reference too, and
// it owns its row.
func TestLeafLink(t *testing.T) {
	link := &pb.Tile{Id: "b/9", Kind: KindText, LinkTargetId: "a/42", Reference: true}
	if !LeafLink(link) {
		t.Error("a row with a target is a leaf link")
	}
	mount := &pb.Tile{Id: "b/1", Kind: KindWell, ChildGridId: "a/7", Reference: true}
	if LeafLink(mount) {
		t.Error("an exit well is not a leaf link")
	}
	owned := &pb.Tile{Id: "a/42", Kind: KindText}
	if LeafLink(owned) {
		t.Error("a row that owns its content is not a leaf link")
	}
}

// TestDescentOf pins the one classification the bar slot and the input layer
// both read, including the tile a shell page row would make ambiguous: a
// serves_page shell is web content, because the page door answers for it.
func TestDescentOf(t *testing.T) {
	cases := []struct {
		name string
		tile *pb.Tile
		want Descent
	}{
		{"url tile", &pb.Tile{Kind: KindURL}, DescentURL},
		{"page tile", &pb.Tile{Kind: KindText, ServesPage: true}, DescentURL},
		{"shell tile", &pb.Tile{Kind: KindShell}, DescentShell},
		{"shell page row", &pb.Tile{Kind: KindShell, ServesPage: true}, DescentURL},
		{"text document", &pb.Tile{Kind: KindText}, DescentNone},
		{"well", &pb.Tile{Kind: KindWell}, DescentNone},
		{"unresolved", nil, DescentNone},
	}
	for _, c := range cases {
		if got := DescentOf(c.tile); got != c.want {
			t.Errorf("%s: DescentOf = %v, want %v", c.name, got, c.want)
		}
	}
}
