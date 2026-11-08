package pane

import "github.com/josephburnett/gridwell/api/rpc"

// BorderColors is the renderer's palette, one saturated and one faded string
// per Family. The functions here are pure and indifferent to which CSS colors
// they are.
type BorderColors struct {
	Focused, FocusedFaded     string
	Text, TextFaded           string
	URL, URLFaded             string
	URLLive, URLLiveFaded     string
	Shell, ShellFaded         string
	Directory, DirectoryFaded string
	Ephemeral, EphemeralFaded string
}

// BorderInput is what the classifier needs about a pane. It is a struct rather
// than a *Pane so this package depends on no cache or tile types: the caller
// resolves the descended tile and its kind first.
type BorderInput struct {
	HasTextFocus bool // the pane's place is a content frame
	DescentDepth int  // greater than 0 means inside at least one door
	TileKnown    bool // the descended row is cached, so TileKind is meaningful
	TileKind     string
	Focused      bool // the keyboard-focused pane in the split tree
	URLLive      bool // a live native view renders into this pane
	// InDirectory is whether the viewed grid is a directory
	// (cache.Grid.Directory). It drives the Directory family for the grid and
	// for a text tile in it; a url or shell tile in one keeps its kind's.
	InDirectory bool
	// Ephemeral is the descended tile living in the scratch grid, so it is
	// deleted on ascent. Only meaningful with HasTextFocus and TileKnown.
	Ephemeral bool
}

// Family is the one classification behind the color grammar. The pane outline
// and the bottom bar's band and buttons both derive from it, so the outline and
// the bar cannot disagree about what the pane is showing.
type Family int

const (
	FamilyGrid      Family = iota // any grid of home's or a far node's, at any depth
	FamilyText                    // a descent into a text tile
	FamilyURL                     // a descent into a url tile, frozen
	FamilyURLLive                 // a descent into a url tile with a live view
	FamilyShell                   // a descent into a shell tile
	FamilyDirectory               // a directory, or a text tile in one: outside the system
	// FamilyEphemeral is a descent into a scratch-grid tile. It beats the kind
	// color, because ascending deletes the tile.
	FamilyEphemeral
)

// FamilyOf is the single classifier every color consumer derives from.
func FamilyOf(s BorderInput) Family {
	if s.HasTextFocus {
		if s.TileKnown {
			if s.Ephemeral {
				return FamilyEphemeral
			}
			switch k := rpc.KindOf(s.TileKind); {
			case k.Live == rpc.DescentURL:
				if s.URLLive {
					return FamilyURLLive
				}
				return FamilyURL
			case k.Live == rpc.DescentShell:
				return FamilyShell
			case k.Content == rpc.ContentBody:
				if s.InDirectory {
					return FamilyDirectory
				}
				return FamilyText
			}
		}
	}
	if s.InDirectory {
		return FamilyDirectory
	}
	return FamilyGrid
}

// BorderColor is the pane's Family, saturated when the pane has focus.
func BorderColor(s BorderInput, c BorderColors) string {
	switch FamilyOf(s) {
	case FamilyEphemeral:
		return focused(s, c.Ephemeral, c.EphemeralFaded)
	case FamilyURLLive:
		return focused(s, c.URLLive, c.URLLiveFaded)
	case FamilyURL:
		return focused(s, c.URL, c.URLFaded)
	case FamilyShell:
		return focused(s, c.Shell, c.ShellFaded)
	case FamilyDirectory:
		return focused(s, c.Directory, c.DirectoryFaded)
	case FamilyText:
		return focused(s, c.Text, c.TextFaded)
	}
	return focused(s, c.Focused, c.FocusedFaded)
}

func focused(s BorderInput, sat, faded string) string {
	if s.Focused {
		return sat
	}
	return faded
}
