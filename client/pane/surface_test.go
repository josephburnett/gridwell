package pane

import "testing"

func TestSurfaceOf(t *testing.T) {
	cases := []struct {
		name          string
		onScreen      bool
		paneContentID string
		descentID     string
		want          SurfaceVerdict
	}{
		{"in the descent it was opened for", true, "u1/7", "u1/7", SurfaceShow},
		{"a live link: the frame is the link row", true, "u1/9", "u1/9", SurfaceShow},
		{"pane ascended to its grid", true, "", "u1/7", SurfaceOrphan},
		{"pane descended elsewhere (a stacked visit)", true, "u1/8", "u1/7", SurfaceOrphan},
		{"pane closed", true, "", "u1/7", SurfaceOrphan},
		{"parked in a stacked level, still in its descent", false, "u1/7", "u1/7", SurfacePark},
		{"parked in a stacked level, moved on", false, "u1/8", "u1/7", SurfacePark},
		{"parked and gone from the tree", false, "", "u1/7", SurfacePark},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SurfaceOf(c.onScreen, c.paneContentID, c.descentID); got != c.want {
				t.Errorf("SurfaceOf(%v, %q, %q) = %v, want %v",
					c.onScreen, c.paneContentID, c.descentID, got, c.want)
			}
		})
	}
}

// A pane in the current tree, descended into a tile, is the same fact
// StillDescended answers: the two must never disagree, or an async descent
// path and the per-frame sweep would take opposite views of the same pane.
func TestSurfaceOfAgreesWithStillDescended(t *testing.T) {
	p := &Pane{ID: "p1"}
	p.Stack = StackAt("g1", nil, "u1/7")
	for _, id := range []string{"u1/7", "u1/8", ""} {
		still := StillDescended(p, id)
		shown := SurfaceOf(true, p.ContentID(), id) == SurfaceShow
		if still != shown {
			t.Errorf("descent %q: StillDescended=%v but SurfaceOf shows=%v", id, still, shown)
		}
	}
	// A closed pane (nil) is never still descended, and its surface — held in
	// a.locals until forgetPane runs — is an orphan wherever it is laid out.
	if StillDescended(nil, "u1/7") || SurfaceOf(true, "", "u1/7") == SurfaceShow {
		t.Error("a closed pane must never read as descended")
	}
}
