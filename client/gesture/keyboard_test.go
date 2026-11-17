package gesture

import "testing"

// Every cell: an input open or not, times every surface, times whether its
// element is up.
func TestKeyboardEveryCell(t *testing.T) {
	surfaces := []Surface{SurfaceFace, SurfaceRawText, SurfaceRendered, SurfaceShell, SurfaceURL}
	if len(surfaces) != int(SurfaceURL)+1 {
		t.Fatal("a surface is missing from the table")
	}
	want := map[Surface][2]OwnerKind{
		// [down, up]
		SurfaceFace:     {OwnerCanvas, OwnerCanvas},
		SurfaceRawText:  {OwnerCanvas, OwnerEditor},
		SurfaceRendered: {OwnerCanvas, OwnerCanvas},
		SurfaceShell:    {OwnerCanvas, OwnerTerminal},
		SurfaceURL:      {OwnerCanvas, OwnerPage},
	}
	for _, s := range surfaces {
		for i, up := range []bool{false, true} {
			for _, input := range []bool{false, true} {
				got := Keyboard(KeyboardState{InputOpen: input, Pane: "p1", Surface: s, Up: up})
				w := Owner{want[s][i], "p1"}
				if input {
					w = Owner{OwnerInput, "p1"}
				}
				if got != w {
					t.Errorf("Keyboard(input=%v, %v, up=%v) = %+v, want %+v", input, s, up, got, w)
				}
			}
		}
	}
}
