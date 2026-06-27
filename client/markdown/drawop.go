package markdown

// DrawOp is the output of the layout pass: a positioned drawing primitive in
// logical (pre-zoom) pixel coordinates. The wasm painter walks a []DrawOp,
// multiplies coordinates by the current zoom scale, and emits canvas calls —
// it does no layout of its own. Keeping layout (wrapping, table column widths,
// list indents, height math) in this pure form is what makes it unit-testable
// against a deterministic Measure.

// ColorRole is a semantic color slot the painter resolves to a concrete CSS
// color. Layout stays free of CSS strings (so it's testable and the renderer
// owns the palette); syntax-highlight token roles live here too (Phase 7).
type ColorRole int

const (
	ColorText ColorRole = iota
	ColorHeading
	ColorCode
	ColorLink
	ColorMuted // blockquote text, list markers
	ColorRuleLine
	ColorCodeBg       // code block panel
	ColorInlineCodeBg // inline `code` chip (lighter than the block panel)
	ColorQuoteBar
	ColorTableHeaderBg
	ColorTableGrid

	// Syntax-highlight token roles (Phase 7). Kept contiguous at the end so
	// the painter can map a chroma token category to one of these.
	ColorSynKeyword
	ColorSynString
	ColorSynComment
	ColorSynNumber
	ColorSynType
	ColorSynFunction
)

// DrawOpKind is the kind of a positioned primitive.
type DrawOpKind int

const (
	// OpText is a positioned run of text. Hrefs make it a link hit target.
	OpText DrawOpKind = iota
	// OpRect is a filled rectangle: code background, blockquote bar, table
	// header background, table gridlines.
	OpRect
	// OpRule is a horizontal rule line spanning the content width.
	OpRule
	// OpImage is an external image to fetch + draw (Phase 6).
	OpImage
	// OpEmbed is a native tile embed, painted by the wasm embed drawer and
	// registered as a descent hit target (preserves the existing embed path).
	OpEmbed
)

// DrawOp is one positioned primitive in logical pixels. Fields not relevant to
// a kind stay zero/empty.
type DrawOp struct {
	Kind DrawOpKind
	X, Y float64 // top-left in logical px
	W, H float64 // size (OpRect/OpRule/OpImage/OpEmbed)

	// OpText:
	Text   string
	FontPx float64 // logical font size
	Style  SpanStyle
	Mono   bool // monospace family (code/code block)

	Color ColorRole

	// Links/embeds/images:
	Href string // OpText link target / OpEmbed descent href
	Src  string // OpImage source URL
	Alt  string // OpImage / OpEmbed alt text

	// OpText source mapping for the rendered-mode caret: SrcStart is the source
	// byte offset of this run's first character, SrcLen the source length it
	// spans. SrcLen == len(Text) for a verbatim run (the caret maps linearly,
	// byte i of Text ↔ SrcStart+i); SrcLen == 0 for an opaque run (inline code,
	// list marker, code block, break) the caret skips. Zero on non-OpText ops.
	SrcStart, SrcLen int
}

// LayoutResult is the full output of the layout pass for one document at one
// content width: the draw ops plus the total content height (for scrollbars).
type LayoutResult struct {
	Ops    []DrawOp
	Height float64
}

// Measure returns the rendered logical-pixel width of text at the given font
// size, style, and family. The wasm renderer backs this with canvas
// measureText; tests pass a deterministic monospace stub so layout geometry is
// fully assertable.
type Measure func(text string, fontPx float64, style SpanStyle, mono bool) float64
