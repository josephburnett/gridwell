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
	pl := PluginInfo{Label: "files", RootGridID: "fs-uuid/1"}
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
