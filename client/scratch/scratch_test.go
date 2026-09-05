package scratch

import "testing"

// The regression this table exists for: an uncached grid answers UNKNOWN. The
// deleted fallback guessed from the local plugin roster, keyed on a qualified
// id's first segment — which for a mounted remote grid (n1/c1/…) is the local
// node — so an uncached remote grid resolved to the LOCAL node's scratch
// grid. Every reader then decided about the wrong node: ascent did not delete
// the visit it should have, a visit's url state was persisted as durable, and
// a pane could be re-anchored into the scratch grid.
func TestFor(t *testing.T) {
	cases := []struct {
		name      string
		in        Grid
		wantID    string
		wantKnown bool
	}{
		{"an uncached grid is not known yet", Grid{}, "", false},
		{"a cached grid answers its own stamp",
			Grid{Cached: true, ScratchGridID: "n1/c1/9"}, "n1/c1/9", true},
		{"a cached grid with no stamp answers nowhere, and knows it",
			Grid{Cached: true}, "", true},
	}
	for _, c := range cases {
		id, known := For(c.in)
		if id != c.wantID || known != c.wantKnown {
			t.Errorf("%s: For(%+v) = (%q, %v), want (%q, %v)",
				c.name, c.in, id, known, c.wantID, c.wantKnown)
		}
	}
}

func TestEphemeral(t *testing.T) {
	// The pane stands on a mounted remote grid whose first segment is the
	// local node id. Uncached, the only honest answer is "not known" — never
	// the local node's scratch grid, which is what the id's shape invites.
	const localScratch = "n1/2"
	remote := Grid{Cached: true, ScratchGridID: "n1/c1/2"}

	cases := []struct {
		name       string
		g          Grid
		tileGridID string
		wantEph    bool
		wantKnown  bool
	}{
		{"uncached: unknown, and not a yes", Grid{}, localScratch, false, false},
		{"uncached: unknown even for the far scratch grid", Grid{}, "n1/c1/2", false, false},
		{"the far node's own scratch grid is the ephemeral one",
			remote, "n1/c1/2", true, true},
		{"the LOCAL scratch grid is not this pane's",
			remote, localScratch, false, true},
		{"an ordinary grid is not ephemeral", remote, "n1/c1/1", false, true},
		{"a cached grid with no scratch grid has no ephemeral tiles",
			Grid{Cached: true}, "", false, true},
	}
	for _, c := range cases {
		eph, known := Ephemeral(c.g, c.tileGridID)
		if eph != c.wantEph || known != c.wantKnown {
			t.Errorf("%s: Ephemeral(%+v, %q) = (%v, %v), want (%v, %v)",
				c.name, c.g, c.tileGridID, eph, known, c.wantEph, c.wantKnown)
		}
	}
}
