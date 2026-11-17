package gesture

// Typing goes to exactly one surface, the keyboard's owner, and it is a
// function of state, never of what happened before (CLAUDE.md, the
// click-lands decision, 2026-11-17).

// OwnerKind is what holds the keyboard.
type OwnerKind int

const (
	// OwnerCanvas: a grid, a rendered, info or hex text, a frozen or parked
	// tile, a pane with nothing live.
	OwnerCanvas OwnerKind = iota
	// OwnerInput: the rename input or the entry modal's, while it is open.
	OwnerInput
	// OwnerEditor: the raw editor of a text shown raw.
	OwnerEditor
	// OwnerTerminal: the terminal of a live, shown shell.
	OwnerTerminal
	// OwnerPage: the page of a live url view that is not parked, which holds
	// OS focus in its own webContents.
	OwnerPage
)

// Owner is the surface typing goes to, and the pane it serves; an input's is
// the focused pane's too, though it names no surface of it.
type Owner struct {
	Kind OwnerKind
	Pane string
}

// KeyboardState is what the owner reads. Up is the focused pane's surface
// element being there to type into: the editor shown, the terminal shown, the
// url view not parked (pane.ParkSurface). The editor keeps the keyboard while
// a gesture clips it, since its park is a clip that keeps the caret
// (overlayClip), so its Up is whether it is shown at all.
type KeyboardState struct {
	InputOpen bool
	Pane      string
	Surface   Surface
	Up        bool
}

// Keyboard is the one derivation of the keyboard's owner.
func Keyboard(s KeyboardState) Owner {
	if s.InputOpen {
		return Owner{OwnerInput, s.Pane}
	}
	switch s.Surface {
	case SurfaceRawText:
		if s.Up {
			return Owner{OwnerEditor, s.Pane}
		}
	case SurfaceShell:
		if s.Up {
			return Owner{OwnerTerminal, s.Pane}
		}
	case SurfaceURL:
		if s.Up {
			return Owner{OwnerPage, s.Pane}
		}
	case SurfaceFace, SurfaceRendered:
	}
	return Owner{OwnerCanvas, s.Pane}
}
