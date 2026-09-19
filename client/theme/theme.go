// Package theme owns the client's colors. Two palettes, one struct, one
// default, and no other spelling of a color anywhere the client draws: canvas,
// DOM chrome, the terminal, and the rendered-document stylesheet all read a
// Palette. It is a view preference, never a fact about the user's things, so
// nothing here reaches the store or the wire.
package theme

import (
	"reflect"
	"strings"
)

// Theme names a palette.
type Theme int

const (
	Dark Theme = iota
	Light
)

// Default is what a client with no stored preference wears.
func Default() Theme { return Dark }

// All is every theme a user may pick, in menu order.
func All() []Theme { return []Theme{Dark, Light} }

// String is the theme's one spelling outside this package: the stored
// preference, the menu item's id, and the e2e hook all read it.
func (t Theme) String() string {
	if t == Light {
		return "light"
	}
	return "dark"
}

// Label is what a menu row reads.
func (t Theme) Label() string {
	if t == Light {
		return "Light mode"
	}
	return "Dark mode"
}

// Parse reads String back. An absent or unreadable preference is not something
// the user can act on, so it lands on Default and says it was not a choice.
func Parse(s string) (Theme, bool) {
	switch s {
	case "dark":
		return Dark, true
	case "light":
		return Light, true
	}
	return Default(), false
}

// Palette is every color the client draws, once. A field is a role, not a
// shade: the two palettes differ in value and never in which roles exist, so a
// drawing site cannot ask for a color one theme has and the other does not.
type Palette struct {
	// The window's ground and a text tile's reading body.
	Bg          string
	FileInnerBg string
	// Error rows read as alarm; Info rows, an expected reconciliation, take the
	// focus-blue family so they read as a note.
	ErrStripBg    string
	ErrStripText  string
	InfoStripBg   string
	InfoStripText string
	PaneBorder    string
	FocusBorder   string
	// FocusBorderFaded outlines a descended but unfocused pane, so the focused
	// one pops while the others stay visibly inside something.
	FocusBorderFaded string
	// PluginBorder is the warm brown of plugin and host identity: an earth-tone
	// ground reading as a boundary, not a grid you can place in.
	PluginBorder      string
	PluginBorderFaded string
	// PluginFill is host content's body, so it does not read as editable.
	PluginFill string
	// Grid lines are uniformly blue: every grid is a grid, whatever owns it.
	GridLineInterior string
	// Each kind has its own identity: text olive green, url purple.
	MarkdownFill      string
	MarkdownLine      string
	MarkdownLineFaded string
	URLFill           string
	URLLine           string
	URLLineFaded      string
	// URLLiveLine is a url tile with a native view attached: the same purple,
	// stronger, and its faded variant stays stronger than the frozen one, so
	// live against frozen reads across unfocused panes.
	URLLiveLine      string
	URLLiveLineFaded string
	// ShellBorder: bash runs outside Gridwell's data world, so it gets its own
	// warm hue, not plugin brown.
	ShellBorder      string
	ShellBorderFaded string
	// ShellFill is the body behind a shell tile's preview or glyph.
	ShellFill string
	// Pane tiles: teal, a hue no other kind uses.
	PaneTileFill   string
	PaneTileBorder string
	// EphemeralBorder overrides the kind color, because ascending deletes the
	// tile, a shell's tmux session included, and the border is the warning.
	EphemeralBorder      string
	EphemeralBorderFaded string
	// SourceLabelBg is opaque enough that a label reads over any preview.
	SourceLabelBg string
	// NoEntry{Fill,Stroke} draw the no-entry badge on a rejected drop.
	NoEntryFill   string
	NoEntryStroke string
	Locked        string
	Selected      string
	// Trace reads stronger than the selection gold, so both can show at once.
	Trace   string
	EdgeDot string
	PlusBg  string
	// PlusBgHi is the + button under the cursor.
	PlusBgHi string
	// PlusBgDelete confirms that a release over the trashcan deletes.
	PlusBgDelete string
	PlusFg       string
	// NoLiveFg dims the slashed go-live glyph where caps.LiveURL is false.
	NoLiveFg      string
	MenuBg        string
	MenuItemHi    string
	Muted         string
	TileResize    string
	SwapArrow     string
	CloseWarn     string
	SplitInactive string
	// ScrimStroke casings a preview line against whatever it crosses, so the
	// line stays visible over a tile body of any shade.
	ScrimStroke string
	// Doorway tints for a non-enterable row (client/pluginhealth). Broken,
	// every failure whatever the reason, takes the red alarm family; waiting
	// takes neutral grey, because nothing has gone wrong yet.
	DoorwayBrokenTint  string
	DoorwayWaitingTint string
	// A dead link (client/deadref) is a state, not a failure, so it gets no
	// alarm color: the veil fades the tile back toward the background, and the
	// outline and label are redrawn muted, keeping the dash and the name.
	DeadLinkVeil string
	DeadLink     string

	// The bar's band per pane family; the url and shell families wear their
	// kind's fill, so they have no field of their own.
	BarGridBand      string
	BarTextBand      string
	BarPluginBand    string
	BarEphemeralBand string
	// BarInk is the bar's own lettering and the circle button's rim, over a
	// band of any family.
	BarInk string
	// CrumbIdle is a crumb that is not where you are: a pane-tile level you
	// are outside, and the outline of one whose row has not arrived yet.
	CrumbIdle string
	// The bar chip on a pane whose room is a memory.
	CachedChipBg string
	CachedChipFg string

	// TextFg is the user's own bytes wherever they are painted raw: the canvas
	// source painter, the editing textarea, and the terminal.
	TextFg      string
	ShellCursor string
	// PreviewLetterbox fills what a frozen capture's aspect ratio leaves over.
	PreviewLetterbox string

	// The rendered-document stylesheet (client/markdown), worn by both the
	// focused overlay and the rasterized preview.
	DocFg       string
	DocLink     string
	DocCodeBg   string
	DocQuoteBar string
	DocQuoteFg  string
	DocRule     string

	// DOM chrome: the url modal, the boot overlay, the circle's own menu.
	Scrim      string
	Border     string
	Surface    string
	SurfaceHi  string
	PrimaryHi  string
	Dim        string
	SubtleText string
	CardShadow string
}

var dark = Palette{
	Bg:          "#0c0d11",
	FileInnerBg: "#1c1f26",

	ErrStripBg:    "#3a1216",
	ErrStripText:  "#ff9a9a",
	InfoStripBg:   "#16203a",
	InfoStripText: "#9ab0ff",

	PaneBorder:       "#1f2229",
	FocusBorder:      "#4a6fff",
	FocusBorderFaded: "#2c3d70",

	PluginBorder:      "#7a6a4a",
	PluginBorderFaded: "#4a4233",
	PluginFill:        "#2a2419",

	GridLineInterior: "#1c2540",

	MarkdownFill:      "#2c3a1a",
	MarkdownLine:      "#8aa05a",
	MarkdownLineFaded: "#4a5a3a",
	URLFill:           "#2b1a3a",
	URLLine:           "#7a5a9a",
	URLLineFaded:      "#4a3a5a",
	URLLiveLine:       "#a07acc",
	URLLiveLineFaded:  "#5c4478",

	ShellBorder:      "#d4863a",
	ShellBorderFaded: "#6e4a22",
	ShellFill:        "#2e220f",

	PaneTileFill:   "#10282b",
	PaneTileBorder: "#3aa8a8",

	EphemeralBorder:      "#8b8e96",
	EphemeralBorderFaded: "#4b4d52",

	SourceLabelBg: "rgba(20, 12, 8, 0.78)",
	NoEntryFill:   "#c93030",
	NoEntryStroke: "#f6f6f6",
	Locked:        "#26262a",
	Selected:      "#e3b16f",
	Trace:         "#ffd94a",
	EdgeDot:       "#5a6a8a",
	PlusBg:        "#23252d",
	PlusBgHi:      "#2d3140",
	PlusBgDelete:  "#6e2b22",
	PlusFg:        "#c8c9ce",
	NoLiveFg:      "#787b84",
	MenuBg:        "#16181f",
	MenuItemHi:    "#e8e9ee",
	Muted:         "#6c6f78",
	TileResize:    "#4a6fff",
	SwapArrow:     "#4a6fff",
	CloseWarn:     "#e0727a",
	SplitInactive: "#6c6f78",
	ScrimStroke:   "rgba(0, 0, 0, 0.55)",

	DoorwayBrokenTint:  "rgba(180, 40, 40, 0.38)",
	DoorwayWaitingTint: "rgba(40, 40, 46, 0.55)",
	DeadLinkVeil:       "rgba(12, 13, 17, 0.72)",
	DeadLink:           "#5c5f68",

	BarGridBand:      "#151b2e",
	BarTextBand:      "#1b2213",
	BarPluginBand:    "#241e12",
	BarEphemeralBand: "#1d1f24",
	BarInk:           "#dff4f4",
	CrumbIdle:        "#1d4a4a",
	CachedChipBg:     "#8a6d2f",
	CachedChipFg:     "#f4e3b2",

	TextFg:           "#d8d9de",
	ShellCursor:      "#c87a5a",
	PreviewLetterbox: "#000000",

	DocFg:       "#d8d9de",
	DocLink:     "#7a9fd4",
	DocCodeBg:   "#1c1d24",
	DocQuoteBar: "#3a4b5a",
	DocQuoteFg:  "#9ca0ad",
	DocRule:     "#3a4150",

	Scrim:      "rgba(12, 13, 17, 0.72)",
	Border:     "#292c36",
	Surface:    "#1c1f29",
	SurfaceHi:  "#232634",
	PrimaryHi:  "#5b7eff",
	Dim:        "#8a8d97",
	SubtleText: "#b6b8c0",
	CardShadow: "rgba(0, 0, 0, 0.6)",
}

// light keeps every role's hue and inverts its lightness: grounds go near
// white, ink near black, and each kind's identity hue darkens enough to carry
// a 1px outline on a pale body. The accent blue is the one shade held across
// both palettes, because it is the app's one constant.
var light = Palette{
	Bg:          "#f6f7fa",
	FileInnerBg: "#ffffff",

	ErrStripBg:    "#fbe0e2",
	ErrStripText:  "#9c1620",
	InfoStripBg:   "#e5eaff",
	InfoStripText: "#25407e",

	PaneBorder:       "#d5d8de",
	FocusBorder:      "#4a6fff",
	FocusBorderFaded: "#a9b8ff",

	PluginBorder:      "#8a7748",
	PluginBorderFaded: "#c3b890",
	PluginFill:        "#f3ecdc",

	GridLineInterior: "#d6dcec",

	MarkdownFill:      "#ecf3dc",
	MarkdownLine:      "#5f7430",
	MarkdownLineFaded: "#a7b783",
	URLFill:           "#f1e7f9",
	URLLine:           "#6d3f99",
	URLLineFaded:      "#b193ca",
	URLLiveLine:       "#8b3fd6",
	URLLiveLineFaded:  "#c09ce2",

	ShellBorder:      "#a2570f",
	ShellBorderFaded: "#d6ab78",
	ShellFill:        "#fcf0dc",

	PaneTileFill:   "#e0f2f2",
	PaneTileBorder: "#1a7a7a",

	EphemeralBorder:      "#70747d",
	EphemeralBorderFaded: "#b3b7bf",

	SourceLabelBg: "rgba(252, 250, 246, 0.86)",
	NoEntryFill:   "#c93030",
	NoEntryStroke: "#ffffff",
	Locked:        "#e3e3e8",
	Selected:      "#a8731a",
	Trace:         "#7f4300",
	EdgeDot:       "#7d8aa6",
	PlusBg:        "#e7e8ee",
	PlusBgHi:      "#d4d8e6",
	PlusBgDelete:  "#f0b2aa",
	PlusFg:        "#3a3c44",
	NoLiveFg:      "#8d9099",
	MenuBg:        "#ffffff",
	MenuItemHi:    "#16181f",
	Muted:         "#83868e",
	TileResize:    "#4a6fff",
	SwapArrow:     "#4a6fff",
	CloseWarn:     "#c02e3a",
	SplitInactive: "#9a9da5",
	ScrimStroke:   "rgba(255, 255, 255, 0.72)",

	DoorwayBrokenTint:  "rgba(200, 60, 60, 0.24)",
	DoorwayWaitingTint: "rgba(210, 210, 216, 0.55)",
	DeadLinkVeil:       "rgba(246, 247, 250, 0.72)",
	DeadLink:           "#8d9099",

	BarGridBand:      "#e3e9f8",
	BarTextBand:      "#eef4e0",
	BarPluginBand:    "#f5efe2",
	BarEphemeralBand: "#eceef2",
	BarInk:           "#1d2e2e",
	CrumbIdle:        "#bfe0e0",
	CachedChipBg:     "#e6c67c",
	CachedChipFg:     "#4a3708",

	TextFg:           "#23252c",
	ShellCursor:      "#a3502a",
	PreviewLetterbox: "#e6e7ec",

	DocFg:       "#23252c",
	DocLink:     "#2a5ba8",
	DocCodeBg:   "#eef0f5",
	DocQuoteBar: "#c1cbd6",
	DocQuoteFg:  "#6a6e79",
	DocRule:     "#d2d6df",

	Scrim:      "rgba(120, 124, 136, 0.45)",
	Border:     "#d5d8e0",
	Surface:    "#edeff4",
	SurfaceHi:  "#e0e3ec",
	PrimaryHi:  "#3559e8",
	Dim:        "#6f727b",
	SubtleText: "#4a4d56",
	CardShadow: "rgba(40, 44, 60, 0.22)",
}

// Of is the one lookup from a theme to its colors.
func Of(t Theme) Palette {
	if t == Light {
		return light
	}
	return dark
}

// CSSVar is one custom property: the DOM stylesheet in web/index.html names
// these, so a color it wears has no second spelling there.
type CSSVar struct {
	Name  string
	Value string
}

// CSSVars renders the whole palette, so a field added here reaches the DOM
// without a second list to keep in step. --gw-file-inner-bg is FileInnerBg.
func (p Palette) CSSVars() []CSSVar {
	v := reflect.ValueOf(p)
	t := v.Type()
	out := make([]CSSVar, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		out = append(out, CSSVar{Name: cssName(t.Field(i).Name), Value: v.Field(i).String()})
	}
	return out
}

// cssName lowercases a Go field name into --gw-kebab-case, treating a run of
// capitals (URL, DOM) as one word.
func cssName(field string) string {
	var b strings.Builder
	b.WriteString("--gw")
	runs := []rune(field)
	for i, r := range runs {
		upper := r >= 'A' && r <= 'Z'
		if upper && (i == 0 || !isUpper(runs[i-1]) || (i+1 < len(runs) && !isUpper(runs[i+1]))) {
			b.WriteByte('-')
		}
		if upper {
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }
