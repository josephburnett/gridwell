package markdown

// Interactive task-list checkboxes: the rendered view's <input> elements
// map back to "[ ]"/"[x]" markers in the SOURCE, and toggling one edits the
// source through the normal text-edit door (the content-store entry — the
// wasm overlay owns that wiring; this file owns the pure mapping).
//
// The mapping invariant: the N-th checkbox the renderer emits corresponds
// to the N-th TaskCheckBox node in the parsed AST, because RenderHTML and
// this scan share ONE parser configuration (gmRenderer — the same "one
// dialect" rule doctype.AltFromSource leans on). A literal "- [ ]" inside a code
// fence is not a TaskCheckBox in either place, so it can't shift the
// numbering. TestToggleTaskRenderParity pins the invariant.

import (
	"github.com/yuin/goldmark/ast"
	east "github.com/yuin/goldmark/extension/ast"
	gmtext "github.com/yuin/goldmark/text"
)

// taskMarkerOffsets returns the source byte offset of every task-list
// marker's "[", in document order — the order the rendered view's
// checkboxes appear in the DOM.
func taskMarkerOffsets(src []byte) []int {
	root := gmRenderer.Parser().Parse(gmtext.NewReader(src))
	var offs []int
	_ = ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if _, ok := n.(*east.TaskCheckBox); !ok {
			return ast.WalkContinue, nil
		}
		// A TaskCheckBox is the first inline of its list item's first text
		// block; that block's first line segment starts at the "[" (the
		// list marker and indentation are consumed by the block parser).
		p := n.Parent()
		if p == nil || p.Lines().Len() == 0 {
			return ast.WalkContinue, nil
		}
		if start := p.Lines().At(0).Start; isTaskMarker(src, start) {
			offs = append(offs, start)
		}
		return ast.WalkContinue, nil
	})
	return offs
}

// isTaskMarker double-checks the bytes at off spell a task marker before
// anything writes there — if the AST's segment arithmetic ever drifts from
// the source, the toggle must refuse rather than corrupt a document.
func isTaskMarker(src []byte, off int) bool {
	return off >= 0 && off+2 < len(src) &&
		src[off] == '[' && src[off+2] == ']' &&
		(src[off+1] == ' ' || src[off+1] == 'x' || src[off+1] == 'X')
}

// TaskCount returns how many toggleable task checkboxes src renders.
func TaskCount(src []byte) int {
	return len(taskMarkerOffsets(src))
}

// ToggleTask flips the index-th (0-based, document order) task checkbox in
// src: "[ ]" becomes "[x]", "[x]"/"[X]" becomes "[ ]". Returns the new
// source and true, or (nil, false) when index addresses no checkbox. The
// input is never mutated; the output differs from it in exactly one byte,
// so every other byte of the document stays as the user left it.
func ToggleTask(src []byte, index int) ([]byte, bool) {
	offs := taskMarkerOffsets(src)
	if index < 0 || index >= len(offs) {
		return nil, false
	}
	out := append([]byte(nil), src...)
	i := offs[index] + 1
	if out[i] == ' ' {
		out[i] = 'x'
	} else {
		out[i] = ' '
	}
	return out, true
}
