package markdown

// This file defines the lowered document model — the tree the goldmark AST is
// lowered into (Lower, Phase 1) and that the layout pass walks (Layout). It
// sits between the parser and the canvas painter so the layout math is pure
// Go and unit-testable: the wasm renderer only paints the resulting DrawOps.

// NodeKind classifies a block-level node in the lowered document tree.
type NodeKind int

const (
	// NodeDocument is the root; Children are the top-level blocks.
	NodeDocument NodeKind = iota
	// NodeParagraph carries inline Spans.
	NodeParagraph
	// NodeHeading carries inline Spans; Level is 1–6.
	NodeHeading
	// NodeCodeBlock is a fenced/indented code block. Lang is the info-string
	// language (may be ""); Spans[0].Text is the raw (already newline-joined)
	// code body with StyleCode.
	NodeCodeBlock
	// NodeBlockQuote contains block Children; quotes nest.
	NodeBlockQuote
	// NodeList contains NodeListItem Children. Ordered + Start describe ordered
	// lists.
	NodeList
	// NodeListItem contains block Children. Checked, when non-nil, marks a GFM
	// task-list item and its checked state.
	NodeListItem
	// NodeThematicBreak is a horizontal rule (no content).
	NodeThematicBreak
	// NodeTable contains NodeTableRow Children (the first row is the header).
	// Align holds per-column alignment.
	NodeTable
	// NodeTableRow contains NodeTableCell Children.
	NodeTableRow
	// NodeTableCell carries inline Spans.
	NodeTableCell
)

// Alignment is a table column's horizontal alignment.
type Alignment int

const (
	AlignNone Alignment = iota
	AlignLeft
	AlignCenter
	AlignRight
)

// Node is a block-level node in the lowered document tree. Leaf blocks
// (paragraph, heading, code block, table cell) carry inline Spans; container
// blocks (document, blockquote, list, list item, table, row) carry Children.
// Unused fields stay at their zero value.
type Node struct {
	Kind     NodeKind
	Spans    []Span // inline content for leaf blocks
	Children []Node // child blocks for container blocks

	Level   int         // NodeHeading: 1–6
	Ordered bool        // NodeList: ordered vs unordered
	Tight   bool        // NodeList: tight (no inter-item spacing) vs loose
	Start   int         // NodeList: first number for an ordered list
	Lang    string      // NodeCodeBlock: info-string language ("" if none)
	Checked *bool       // NodeListItem: non-nil => task item with this state
	Align   []Alignment // NodeTable: per-column alignment
}
