package textedit

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/markdown"
)

// The faces a text tile has, and which one it shows. Both rules read the
// plugin's declared text_presentation and own it between them. They live here
// rather than in client/wasm because `make check` executes this package and
// only compiles that one.

// ToggleVisible decides whether the rendered/raw toggle exists for a text
// tile. A declared text_presentation is the authority. Undeclared, a writable
// doc always toggles and a read-only tile toggles only when its name is
// renderable, since a metadata summary has nothing to flip.
func ToggleVisible(file *gridwellv1.Tile, readOnly bool) bool {
	switch file.TextPresentation {
	case rpc.TextPresentationBoth:
		return true
	case rpc.TextPresentationPlain:
		return false
	}
	return !readOnly || markdown.Renderable(file.AltText)
}

// PresentationHTML routes a text body to its declared renderer. It is the one
// router for both the descent overlay and the grid preview rasterizer, so the
// two faces of a tile cannot disagree.
func PresentationHTML(t *gridwellv1.Tile, body []byte) string {
	if t.TextPresentation == rpc.TextPresentationPlain {
		return markdown.RenderPlainHTML(body)
	}
	return markdown.RenderHTML(body, markdown.IsOrg(t.AltText))
}

// CheckboxClick is what a click on a rendered task-list checkbox does, the
// one interactive control in the read-only rendered view.
type CheckboxClick int

const (
	// CheckboxRevert lets the native flip revert and says nothing: the row is
	// not a document, its org source has no marker mapping, or its bytes have
	// not landed yet, so nothing was ever going to change.
	CheckboxRevert CheckboxClick = iota
	// CheckboxReadOnly reverts and says so, because the user asked for an
	// edit the document cannot take.
	CheckboxReadOnly
	// CheckboxToggle flips the source marker through the edit path.
	CheckboxToggle
)

// DecideCheckboxClick reads the refusals before the edit. Read-only is the
// one refusal worth a word: the other three are states the face already
// shows or a fetch about to land.
func DecideCheckboxClick(textDocument, org, readOnly, bodyCached bool) CheckboxClick {
	switch {
	case !textDocument || org:
		return CheckboxRevert
	case readOnly:
		return CheckboxReadOnly
	case !bodyCached:
		return CheckboxRevert
	}
	return CheckboxToggle
}

// Descent is what a focused text descent shows: the face, and whether the
// raw/rendered toggle rides with it.
type Descent struct {
	Mode   string // "" when nothing text-shaped is descended
	Toggle bool
}

// DecideDescent is the one verdict behind the textarea, the rendered view and
// the file toggle, so a row resolved off the pane's own grid cannot show one
// of them and hide another. A nil tile is a row that has not landed: the mode
// the descent chose stands until it does.
func DecideDescent(tile *gridwellv1.Tile, readOnly bool, paneMode string) Descent {
	if tile == nil {
		return Descent{Mode: ShownMode(paneMode, false)}
	}
	if !rpc.TextDocument(tile) {
		return Descent{}
	}
	return Descent{
		Mode:   ShownMode(paneMode, readOnly),
		Toggle: ToggleVisible(tile, readOnly),
	}
}
