package markdown

import (
	"github.com/josephburnett/gridwell/client/textedit"
	"github.com/yuin/goldmark/ast"
	east "github.com/yuin/goldmark/extension/ast"
)

// edit.go is the rendered-mode editor's keystroke brain: the whole decision
// table for what one key does to (source, caret), pure and unit-tested. The
// wasm layer (client/wasm editRenderedKey) only gathers the inputs, calls
// EditKey, and applies the result — it decides nothing (charter §5).
//
// The editor's contract — the rendered view is a *source editor projected
// through the renderer*, not a WYSIWYG editor:
//
//   - The caret is a source byte offset; every edit is a raw-source splice at
//     that offset. Typing inserts exactly what was typed — no interpretation
//     of markdown syntax on input (no "#"-makes-a-heading handling, no smart
//     list continuation, no formatting shortcuts). What the splice *means* is
//     decided by the markdown parse on the next render, as in the raw editor.
//     Typing inside a heading extends the heading; a new paragraph is plain
//     text. Structural edits (making something bold, restructuring a table)
//     belong in raw mode.
//   - Enter makes a paragraph: it splices a normalized blank line
//     (textedit.InsertParagraphBreak), because a lone "\n" is a soft break
//     markdown renders as a SPACE — the old behavior, where Enter visibly did
//     nothing and the next word glued onto the old paragraph. Two exceptions,
//     both decided by where the caret is: inside a code block Enter is one
//     literal "\n" (a new code line), and inside a table it is a no-op (a
//     blank line would tear the table in half — restructure tables in raw
//     mode).
//   - Backspace/Delete remove exactly one rune — predictable over the raw
//     source. Deleting at a style boundary eats a marker byte; that is the
//     source showing through, same as typing one.
//   - Arrow movement walks caret stops (NextCaretStop/PrevCaretStop): every
//     position the renderer can show, skipping the consumed markers where the
//     caret could neither be drawn faithfully nor type safely. Up/Down and
//     Home/End are geometric over the laid-out ops, like the mouse.

// KeyResult is EditKey's outcome. When Handled is false the key was not the
// editor's (a browser shortcut, a function key) and nothing changed; the
// caller must not preventDefault. Changed distinguishes edits from pure caret
// movement so the caller knows whether to persist.
type KeyResult struct {
	Src     string // new source (== input src when !Changed)
	Caret   int    // new caret byte offset into Src
	Changed bool   // the source changed (persist + re-render)
	Handled bool   // the key was consumed (preventDefault + redraw)
}

// EditKey applies one keystroke to src at caret. key is the DOM
// KeyboardEvent.key value (a single rune for printable keys, else a named
// key). ops/style/m are the current layout of src — the same ops the painter
// drew, so movement agrees with what is on screen. Modifier-chorded keys are
// the caller's concern (it should not call EditKey for them).
func EditKey(src string, caret int, key string, ops []DrawOp, style LayoutStyle, m Measure) KeyResult {
	caret = clampByte(src, caret)
	unchanged := KeyResult{Src: src, Caret: caret, Handled: true}
	edited := func(s string, c int) KeyResult {
		return KeyResult{Src: s, Caret: c, Changed: s != src, Handled: true}
	}
	switch key {
	case "Enter":
		switch blockContextAt(src, caret) {
		case contextCode:
			return edited(textedit.InsertAt(src, "\n", caret))
		case contextTable:
			return unchanged // a blank line would tear the table; raw mode restructures
		default:
			return edited(textedit.InsertParagraphBreak(src, caret))
		}
	case "Tab":
		return edited(textedit.InsertAt(src, "\t", caret))
	case "Backspace":
		return edited(textedit.DeleteBefore(src, caret))
	case "Delete":
		return edited(textedit.DeleteAt(src, caret), caret)
	case "ArrowLeft":
		return KeyResult{Src: src, Caret: PrevCaretStop(ops, src, caret), Handled: true}
	case "ArrowRight":
		return KeyResult{Src: src, Caret: NextCaretStop(ops, src, caret), Handled: true}
	case "ArrowUp":
		return KeyResult{Src: src, Caret: moveVertical(ops, src, caret, style, m, false), Handled: true}
	case "ArrowDown":
		return KeyResult{Src: src, Caret: moveVertical(ops, src, caret, style, m, true), Handled: true}
	case "Home":
		return KeyResult{Src: src, Caret: moveLineEdge(ops, src, caret, style, m, false), Handled: true}
	case "End":
		return KeyResult{Src: src, Caret: moveLineEdge(ops, src, caret, style, m, true), Handled: true}
	}
	if r := []rune(key); len(r) == 1 {
		return edited(textedit.InsertAt(src, key, caret))
	}
	return KeyResult{Src: src, Caret: caret} // not ours (Escape, F-keys, ...)
}

// moveVertical moves the caret one rendered line up or down, geometrically:
// map the offset to its point, aim at the vertical middle of the adjacent
// line, and map back. Returns off unchanged when there is no line to land on.
func moveVertical(ops []DrawOp, src string, off int, style LayoutStyle, m Measure, down bool) int {
	cx, cy, fontPx, ok := PointFromCaret(ops, src, off, style, m)
	if !ok {
		return off
	}
	lh := fontPx * style.LineSpacing
	if lh <= 0 {
		lh = fontPx
	}
	// Aim at the vertical middle of the adjacent line so the nearest-line pick
	// lands there rather than on the current line.
	targetY := cy + fontPx*0.5
	if down {
		targetY += lh
	} else {
		targetY -= lh
	}
	if n, ok := CaretFromPoint(ops, src, cx, targetY, m); ok {
		return n
	}
	return off
}

// moveLineEdge sends the caret to the start (end=false) or end (end=true) of
// its current visual line, geometrically: hit-test far left / far right at the
// caret's own height. CaretFromPoint's far-right rule lands on the line's last
// typed column (including trailing whitespace the renderer dropped).
func moveLineEdge(ops []DrawOp, src string, off int, style LayoutStyle, m Measure, end bool) int {
	_, cy, fontPx, ok := PointFromCaret(ops, src, off, style, m)
	if !ok {
		return off
	}
	x := -1e9
	if end {
		x = 1e9
	}
	if n, ok := CaretFromPoint(ops, src, x, cy+fontPx*0.5, m); ok {
		return n
	}
	return off
}

// blockContext classifies the block containing a source offset, for the Enter
// decision only.
type blockContext int

const (
	contextFlow  blockContext = iota // paragraph, heading, list, quote, ...
	contextCode                      // fenced or indented code block body
	contextTable                     // a GFM table
)

// blockContextAt reports the context of source byte off: inside a code
// block's body, inside a table, or ordinary flowing text. Ranges are
// end-inclusive: the offset just past a code block's last body byte is still
// "in" it — a caret there (the end of the last code line) must get a code
// Enter, not a paragraph break spliced into the fence.
func blockContextAt(src string, off int) blockContext {
	ctx := contextFlow
	b := []byte(src)
	root := parseAST(b)
	ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n.(type) {
		case *ast.FencedCodeBlock, *ast.CodeBlock:
			if start, stop, ok := linesRange(n); ok && off >= start && off <= stop {
				ctx = contextCode
				return ast.WalkStop, nil
			}
			return ast.WalkSkipChildren, nil
		case *east.Table:
			if start, stop, ok := tableRange(n); ok && off >= start && off <= stop {
				ctx = contextTable
				return ast.WalkStop, nil
			}
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return ctx
}

// linesRange is the source range covered by a block's line segments.
func linesRange(n ast.Node) (start, stop int, ok bool) {
	lines := n.Lines()
	if lines.Len() == 0 {
		return 0, 0, false
	}
	return lines.At(0).Start, lines.At(lines.Len() - 1).Stop, true
}

// tableRange is the source range spanned by a table's inline text segments
// (neither the table nor its rows carry line segments of their own, so the
// cells' text bounds the table).
func tableRange(t ast.Node) (start, stop int, ok bool) {
	start, stop = -1, -1
	_ = ast.Walk(t, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if txt, isText := n.(*ast.Text); isText {
			if start < 0 || txt.Segment.Start < start {
				start = txt.Segment.Start
			}
			if txt.Segment.Stop > stop {
				stop = txt.Segment.Stop
			}
		}
		return ast.WalkContinue, nil
	})
	return start, stop, start >= 0
}
