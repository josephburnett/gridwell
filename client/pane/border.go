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
//	Root — the launcher home page (gridless): the brown plugin/home identity.
//	Text / TextFaded — descent into a markdown text tile.
//	URL / URLFaded — descent into a URL tile, frozen preview.
//	URLLive — descent into a URL tile with a live stream open.
//	Shell / ShellFaded — descent into a shell tile (bash runs outside
//	  Gridwell's data world). Orange.
//	Exit / ExitFaded — read-only host content (a text tile inside a source
//	  grid). Brown, echoing the plugin well that led here.
type BorderColors struct {
	Focused, FocusedFaded  string
	Root                   string
	Text, TextFaded        string
	URL, URLFaded, URLLive string
	Shell, ShellFaded      string
	Exit, ExitFaded        string
}

// BorderInput is everything BorderColor needs to know about a pane in
// order to pick its outline color. Carrying a struct instead of *Pane +
// loose flags keeps the function from depending on cache.Grid or
// rpc.Tile types — the caller resolves "is there a descended tile, and
// what kind?" first.
type BorderInput struct {
	// HasTextFocus mirrors pane.Pane.TextFocus != 0: this pane is
	// descended into a content tile (text or url).
	HasTextFocus bool
	// DescentDepth mirrors len(pane.Pane.Path): >0 means we're inside
	// at least one well.
	DescentDepth int
	// TileKnown is true when the descended tile's row is in the
	// client's cache (so TileKind is meaningful).
	TileKnown bool
	// TileKind is the rpc.Kind string: "text", "url", "well",
	// "file-well". Only consulted when TileKnown is true.
	TileKind string
	// Focused is true when this pane is the keyboard-focused pane in
	// the split tree.
	Focused bool
	// URLLive is true when there's an open WebSocket stream rendering
	// a live Chromium tab into this pane. Only meaningful for a
	// descent into a URL tile.
	URLLive bool
	// InSourceGrid is true when the pane's currently-viewed grid is
	// source-backed (fs or proc). Drives the brown Exit border for a
	// read-only host text tile so it echoes the plugin well that led here.
	// (The grid view itself is still blue — every grid is a grid.)
	InSourceGrid bool
	// IsLauncher is true when the pane is at the gridless launcher home
	// (no anchor). The only place the brown Root identity shows.
	IsLauncher bool
}

// BorderColor returns the CSS color string for the pane's outline,
// implementing Gridwell's color grammar:
//
//   - Descent into a URL tile, live   → URLLive
//   - Descent into a URL tile, frozen → URL or URLFaded
//   - Descent into a shell tile       → Shell or ShellFaded
//   - Descent into a read-only host text tile → Exit or ExitFaded
//   - Descent into a text tile        → Text or TextFaded
//   - Launcher home (gridless)        → Root
//   - Any grid (any depth, any plugin) → Focused or FocusedFaded
//
// The pattern is: classify by what's inside, then pick the saturated
// or faded variant based on focus.
func BorderColor(s BorderInput, c BorderColors) string {
	if s.HasTextFocus {
		if s.TileKnown {
			switch s.TileKind {
			case "url":
				if s.URLLive {
					return c.URLLive
				}
				return focused(s, c.URL, c.URLFaded)
			case "shell":
				return focused(s, c.Shell, c.ShellFaded)
			case "text":
				// A text tile that lives in a source-backed grid is a
				// read-only window onto host state (the @info tile in a
				// proc-well, file metadata in an fs-well). Its outline
				// belongs to the Exit (brown) family so the frame keeps
				// echoing the plugin well that put us here.
				if s.InSourceGrid {
					return focused(s, c.Exit, c.ExitFaded)
				}
				return focused(s, c.Text, c.TextFaded)
			}
		}
		return focused(s, c.Focused, c.FocusedFaded)
	}
	// Not descended into a content tile: the launcher home is brown, every
	// grid (any depth, any plugin — including source-backed fs/proc grids)
	// is blue.
	if s.IsLauncher {
		return c.Root
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
