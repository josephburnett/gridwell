package pane

// BorderColors holds the color strings BorderColor maps to. The wasm
// renderer passes in its current palette; the function is pure and
// entirely indifferent to which CSS colors they are.
//
// The grouping reflects Gridwell's color grammar:
//
//	Focused / FocusedFaded — any grid the user is navigating, at any depth
//	  and in any plugin (every grid is blue). Saturated when this pane has
//	  the keyboard / cursor focus, faded otherwise.
//	Text / TextFaded — descent into a markdown text tile.
//	URL / URLFaded — descent into a URL tile, frozen preview.
//	URLLive / URLLiveFaded — descent into a URL tile with a live stream
//	  open.
//	Shell / ShellFaded — descent into a shell tile (bash runs outside
//	  Gridwell's data world). Orange.
//	Exit / ExitFaded — read-only host content (a text tile inside a source
//	  grid). Brown, echoing the plugin well that led here.
//	Ephemeral / EphemeralFaded — descent into an ephemeral (scratch-grid)
//	  tile: gray, overriding the kind color, because ascending deletes the
//	  tile, a shell's tmux session included. The border is the warning not
//	  to start persistent work there.
type BorderColors struct {
	Focused, FocusedFaded     string
	Text, TextFaded           string
	URL, URLFaded             string
	URLLive, URLLiveFaded     string
	Shell, ShellFaded         string
	Exit, ExitFaded           string
	Ephemeral, EphemeralFaded string
}

// BorderInput is everything BorderColor needs to know about a pane in
// order to pick its outline color. Carrying a struct instead of *Pane +
// loose flags keeps the function from depending on cache.Grid or
// rpc.Tile types — the caller resolves "is there a descended tile, and
// what kind?" first.
type BorderInput struct {
	// HasTextFocus mirrors "the pane's place is a content frame": this pane is
	// descended into a content tile (text or url).
	HasTextFocus bool
	// DescentDepth is the pane's descent depth: greater than 0 means the
	// pane is inside at least one well.
	DescentDepth int
	// TileKnown is true when the descended tile's row is in the
	// client's cache (so TileKind is meaningful).
	TileKnown bool
	// TileKind is the rpc.Kind string ("text", "url", "well", "shell",
	// "pane"). Only consulted when TileKnown is true.
	TileKind string
	// Focused is true when this pane is the keyboard-focused pane in
	// the split tree.
	Focused bool
	// URLLive is true when a live native view (WebContentsView) renders
	// a Chromium tab into this pane. Only meaningful for a descent into
	// a URL tile.
	URLLive bool
	// InSourceGrid is true when the pane's currently-viewed grid is
	// source-backed (fs or proc). It drives the brown Exit border for a
	// read-only host text tile, echoing the plugin well that led here. The
	// grid view itself is still blue: every grid is a grid.
	InSourceGrid bool
	// Ephemeral is true when the descended tile lives in the plugin's
	// scratch grid, so it is deleted on ascent. It overrides the kind color
	// with gray, and is only meaningful when HasTextFocus and TileKnown.
	Ephemeral bool
}

// Family names the color family a pane belongs to — the one classification
// behind Gridwell's color grammar. BorderColor picks the pane outline from
// it, and the bottom bar picks its band and button shades from the same fact,
// so the frame and the bar cannot disagree about what the pane is showing.
type Family int

const (
	// FamilyGrid is any grid the user is navigating, at any depth and in
	// any plugin (every grid is blue).
	FamilyGrid Family = iota
	// FamilyText is a descent into a markdown text tile.
	FamilyText
	// FamilyURL is a descent into a URL tile, frozen preview.
	FamilyURL
	// FamilyURLLive is a descent into a URL tile with a live view open.
	FamilyURLLive
	// FamilyShell is a descent into a shell tile.
	FamilyShell
	// FamilyExit is a read-only host content descent (a text tile inside a
	// source grid): brown, echoing the plugin well that led here.
	FamilyExit
	// FamilyEphemeral is a descent into an ephemeral (scratch-grid) tile:
	// gray beats the kind color, because ascending deletes it.
	FamilyEphemeral
)

// FamilyOf classifies the pane by what's inside it. This is the single
// classifier every color consumer derives from.
func FamilyOf(s BorderInput) Family {
	if s.HasTextFocus {
		if s.TileKnown {
			if s.Ephemeral {
				return FamilyEphemeral
			}
			switch s.TileKind {
			case "url":
				if s.URLLive {
					return FamilyURLLive
				}
				return FamilyURL
			case "shell":
				return FamilyShell
			case "text":
				if s.InSourceGrid {
					return FamilyExit
				}
				return FamilyText
			}
		}
		return FamilyGrid
	}
	return FamilyGrid
}

// BorderColor returns the CSS color string for the pane's outline: the
// pane's Family, in the saturated variant when the pane has focus and the
// faded one otherwise.
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
	case FamilyExit:
		return focused(s, c.Exit, c.ExitFaded)
	case FamilyText:
		return focused(s, c.Text, c.TextFaded)
	}
	return focused(s, c.Focused, c.FocusedFaded)
}

// focused returns the saturated color when the pane has focus, else the faded.
func focused(s BorderInput, sat, faded string) string {
	if s.Focused {
		return sat
	}
	return faded
}
