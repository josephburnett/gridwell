package pane

// BorderColors holds the eight color strings BorderColor maps to. The
// wasm renderer passes in its current palette; the function is pure
// and entirely indifferent to which CSS colors they are.
//
// The grouping reflects Gridwell's color grammar:
//
//	Focused / FocusedFaded — descent into a well (or a tile whose kind
//	  isn't known yet). Saturated when this pane has the keyboard /
//	  cursor focus, faded otherwise.
//	Root — the user's root grid: nothing descended.
//	Text / TextFaded — descent into a markdown text tile.
//	URL / URLFaded — descent into a URL tile, frozen preview.
//	URLLive — descent into a URL tile with a live stream open.
type BorderColors struct {
	Focused, FocusedFaded string
	Root                  string
	Text, TextFaded       string
	URL, URLFaded, URLLive string
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
	// "blackhole". Only consulted when TileKnown is true.
	TileKind string
	// Focused is true when this pane is the keyboard-focused pane in
	// the split tree.
	Focused bool
	// URLLive is true when there's an open WebSocket stream rendering
	// a live Chromium tab into this pane. Only meaningful for a
	// descent into a URL tile.
	URLLive bool
}

// BorderColor returns the CSS color string for the pane's outline,
// implementing Gridwell's color grammar:
//
//   - Descent into a URL tile, live  → URLLive
//   - Descent into a URL tile, frozen → URL or URLFaded
//   - Descent into a text tile        → Text or TextFaded
//   - Descent into a well (or descent target not yet cached) → Focused or FocusedFaded
//   - Root grid                       → Root
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
				if s.Focused {
					return c.URL
				}
				return c.URLFaded
			case "text":
				if s.Focused {
					return c.Text
				}
				return c.TextFaded
			}
		}
		if s.Focused {
			return c.Focused
		}
		return c.FocusedFaded
	}
	if s.DescentDepth > 0 {
		if s.Focused {
			return c.Focused
		}
		return c.FocusedFaded
	}
	return c.Root
}
