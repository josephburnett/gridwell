package rpc

import "testing"

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
// the URL-restore walk both rely on. Shell MUST be included (it sets TextFocus
// and is encoded into the URL, so it has to round-trip on reload); the well
// kinds must NOT be (they are grid descents, not text-focus). This is the drift
// that dropped shell descents on reload.
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

// TestSplitID pins the one-hop routing peel every layer shares. Until this
// codec was unified, the server carried its own splitPluginID and two literal
// copies of the peel (session.go, nodeexport.go) whose no-slash handling had
// already diverged from UUIDOf — the exact "same fact, parallel copies" seam
// the charter forbids. These cases are the contract: a chain peels exactly one
// segment; a bare id, an empty id, and a degenerate leading "/" are all
// unqualified (ok=false), never a half-parse.
func TestSplitID(t *testing.T) {
	cases := []struct {
		id, uuid, rest string
		ok             bool
	}{
		{"u/1", "u", "1", true},
		{"ssh1/rp1/7", "ssh1", "rp1/7", true}, // a chain peels ONE segment
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

// TestLocalOf pins the display half of the codec. Until LocalOf existed the
// last-segment strip was re-implemented inline in three client packages
// (wasm input, embed's DefaultAlt, url's Encode) — the same computation,
// three copies. The invariant that makes LocalOf safe everywhere:
// QualifyID(NamespaceOf(id), LocalOf(id)) reproduces any qualified id.
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
		tile Tile
		want bool
	}{
		{"interior well (same plugin) is not an exit well",
			Tile{Kind: KindWell, GridID: "u/1", ChildGridID: "u/2"}, false},
		{"cross-plugin well is an exit well",
			Tile{Kind: KindWell, GridID: "u/1", ChildGridID: "v/2"}, true},
		{"non-well is never an exit well",
			Tile{Kind: KindText, GridID: "u/1", ChildGridID: "v/2"}, false},
		{"well with no child grid is not an exit well",
			Tile{Kind: KindWell, GridID: "u/1"}, false},
		{"synthetic node, both ids empty, is not an exit well",
			Tile{Kind: KindWell}, false},
		// The launcher's exact shape: no owning grid, qualified child grid.
		{"synthetic launcher node (empty GridID, qualified ChildGridID) is an exit well",
			Tile{Kind: KindWell, ChildGridID: "plugin-uuid/1"}, true},
	}
	for _, c := range cases {
		if got := IsExitWell(&c.tile); got != c.want {
			t.Errorf("%s: IsExitWell = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPluginWellTile(t *testing.T) {
	pl := PluginInfo{Label: "files", RootGridID: "fs-uuid/1",
		RootViewCx: 3, RootViewCy: -2, RootViewZoom: 0.5}
	got := PluginWellTile(pl)
	// The load-bearing invariants the launcher preview depends on: the node is
	// a well carrying the plugin's root grid as its child, which makes it an
	// exit well (so drawNodeWithPreview fetches and previews that grid).
	if !IsWellKind(got.Kind) {
		t.Errorf("PluginWellTile kind = %q, want a well", got.Kind)
	}
	if got.ChildGridID != pl.RootGridID {
		t.Errorf("PluginWellTile ChildGridID = %q, want %q", got.ChildGridID, pl.RootGridID)
	}
	if !IsExitWell(&got) {
		t.Errorf("PluginWellTile is not an exit well; launcher would draw an inert interior well")
	}
	if got.AltText != pl.Label {
		t.Errorf("PluginWellTile AltText = %q, want %q", got.AltText, pl.Label)
	}
	// The plugin's persisted root view rides as the tile's framing (the node
	// grid's mapping), so a + menu descent lands at the left-off view.
	if got.ViewCx != 3 || got.ViewCy != -2 || got.ViewZoom != 0.5 {
		t.Errorf("PluginWellTile framing = (%d,%d,%v), want the plugin root view (3,-2,0.5)",
			got.ViewCx, got.ViewCy, got.ViewZoom)
	}
}

// TestKindPartition pins the three descent-class predicates as a PARTITION of
// the descendable kinds: every kind belongs to exactly one of grid descent
// (IsWellKind), text-focus descent (IsContentDescentKind), or workspace
// descent (IsWorkspaceKind). A new kind that joins no class — or two — falls
// through (or double-fires) the click router and the URL-restore walk, the
// exact drift that once dropped shell descents on reload.
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

// TestHomeGrid: "/" means the first configured plugin's root grid, skipping
// plugins without one (broken/rootless) — the boot/URL home derivation
// (owner decision 2026-07-19).
func TestHomeGrid(t *testing.T) {
	first := PluginInfo{UUID: "p1", RootGridID: "p1/1"}
	second := PluginInfo{UUID: "p2", RootGridID: "p2/1"}
	rootless := PluginInfo{UUID: "p0"} // no RootGridID: broken or rootless
	if got := HomeGrid(PluginList{HomeGridID: "n/1", Plugins: []PluginInfo{first}}); got != "n/1" {
		t.Errorf("HomeGrid = %q, want the handshake's home_grid_id", got)
	}
	if got := HomeGrid(PluginList{Plugins: []PluginInfo{rootless, second}}); got != "p2/1" {
		t.Errorf("HomeGrid = %q, want p2/1 (the first rooted row, when the field is absent)", got)
	}
	if got := HomeGrid(PluginList{Plugins: []PluginInfo{rootless}}); got != "" {
		t.Errorf("HomeGrid = %q, want \"\" (nothing rooted)", got)
	}
	if got := HomeGrid(PluginList{}); got != "" {
		t.Errorf("HomeGrid(empty) = %q, want empty", got)
	}
}

// TestContentID: the ONE resolution point for read-through — a leaf link's
// content operations key by its target; an owned tile keys by itself. Every
// client content door (body fetch, edit buffer, save routing, preview fetch,
// shell session, workspace layout) reads this, so a link and its target share
// one content fact by construction.
func TestContentID(t *testing.T) {
	link := Tile{ID: "b/9", Kind: KindText, LinkTargetID: "a/42"}
	if got := link.ContentID(); got != "a/42" {
		t.Errorf("link ContentID = %q, want the target a/42", got)
	}
	owned := Tile{ID: "a/42", Kind: KindText}
	if got := owned.ContentID(); got != "a/42" {
		t.Errorf("owned ContentID = %q, want its own id", got)
	}
}
