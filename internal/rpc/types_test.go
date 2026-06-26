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
