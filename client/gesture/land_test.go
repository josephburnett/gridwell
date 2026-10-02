package gesture

import "testing"

// Every surface a content descent can show, with the press inside and outside
// the text box: what the surface would have done with the press had it been
// there to take it.
func TestLand(t *testing.T) {
	tests := []struct {
		s      Surface
		inText bool
		want   Landing
	}{
		{SurfaceRawText, true, LandCaret},
		{SurfaceRawText, false, LandAscend},
		{SurfaceRendered, true, LandElement},
		{SurfaceRendered, false, LandPane},
		{SurfaceShell, true, LandTerminal},
		{SurfaceShell, false, LandTerminal},
		{SurfaceURL, true, LandPane},
		{SurfaceURL, false, LandPane},
		{SurfaceFace, true, LandPane},
		{SurfaceFace, false, LandPane},
	}
	for _, c := range tests {
		if got := Land(c.s, c.inText); got != c.want {
			t.Errorf("Land(%v, %v) = %v, want %v", c.s, c.inText, got, c.want)
		}
	}
}
