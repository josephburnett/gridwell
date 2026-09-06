package textedit

import (
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/markdown"
)

// The faces a text tile has, and which one it shows. Both rules read
// rpc.Tile.TextPresentation, the plugin's declaration, and they are the two
// owners of it: text_presentation says what faces exist, and the read-only
// derivation says whether the raw one can be typed into. They live here rather
// than in client/wasm because `make check` executes this package and compiles
// that one.

// ToggleVisible decides whether the rendered/raw toggle exists for a text
// tile. The owning plugin's text_presentation is the authority when declared:
// "both" keeps the flip between rendered and raw source, with the textarea
// guard still keeping raw read-only, while "plain" and "rendered" are
// single-presentation. An undeclared tile follows the default rule: a
// writable doc always toggles, and a read-only tile toggles only when its
// name is renderable (an fs .md or .org), because a metadata summary has
// nothing to flip.
func ToggleVisible(file *rpc.Tile, readOnly bool) bool {
	switch file.TextPresentation {
	case rpc.TextPresentationBoth:
		return true
	case rpc.TextPresentationPlain, rpc.TextPresentationRendered:
		return false
	}
	return !readOnly || markdown.Renderable(file.AltText)
}

// PresentationHTML routes a text body to its declared renderer: the plugin's
// text_presentation "plain" shows verbatim preformatted text
// (markdown.RenderPlainHTML), and everything else takes the document
// renderer. One router for the descent overlay and the grid preview
// rasterizer, so the two faces of a tile cannot disagree.
func PresentationHTML(t *rpc.Tile, body []byte) string {
	if t.TextPresentation == rpc.TextPresentationPlain {
		return markdown.RenderPlainHTML(body)
	}
	return markdown.RenderHTML(body, markdown.IsOrg(t.AltText))
}
