//go:build js && wasm

package main

import (
	"strconv"
	"syscall/js"

	"github.com/josephburnett/ascent/client/dragdrop"
	"github.com/josephburnett/ascent/client/pane"
	"github.com/josephburnett/ascent/internal/rpc"
)

// fileSaveDebounceMs is the delay between the first keystroke since
// the last save and the next save fire. Continuous typing therefore
// saves at most once per this interval; a typing pause longer than
// this resolves with one final save shortly after the user stops.
const fileSaveDebounceMs = 600

// scheduleFileSave queues a debounced save of the focused pane's
// textarea contents. Cheap to call from every keystroke — no-op if a
// save is already pending.
func (a *App) scheduleFileSave() {
	if a.fileSaveScheduled {
		return
	}
	a.fileSaveScheduled = true
	js.Global().Call("setTimeout", a.fileSaveCb, fileSaveDebounceMs)
}

// fileMargin returns the pixel inset between the pane edge and the
// inner text-area for a file-focused pane. Equal to the edge-zone
// width so left clicks in the surrounding ring trigger ascent and the
// textarea (when shown) stays clear of those pixels. The grid-pattern
// cue lives in this margin.
func fileMargin(r paneRect) float64 {
	ps := dragdrop.Pane{
		ScreenX: r.X, ScreenY: r.Y, ScreenW: r.W, ScreenH: r.H,
		CellPx: cellPx, Zoom: 1,
	}
	return dragdrop.EdgeBand(ps)
}

// fileOvertakeZoom returns the parent zoom at which the file node's
// footprint (W × H cells) exceeds the inner-box dimensions (textarea
// region) of pane rect r. This is the file-mode analogue of
// zoomtrans.OvertakeZoom: it's the parent zoom the descent transition
// targets, so at the path-swap moment the footprint screen size equals
// the inner-box and content rendered at FileZoom in the textarea matches
// the preview at parent = FileOvertake.
//
// Smaller than the legacy OvertakeZoom (footprint = full pane) by a
// factor of inner-box-vs-pane — i.e., the preview "zooms in a little
// more" so it shows only the meaningful textarea content, not the
// surrounding outer-ring chrome. The "max" choice picks the dimension
// that needs more zoom to fill, so the other dimension overflows the
// inner-box and the user sees a center-crop of the textarea content
// in the preview.
func fileOvertakeZoom(r paneRect, fileW, fileH int64) float64 {
	if fileW <= 0 || fileH <= 0 {
		return 1
	}
	m := fileMargin(r)
	innerW := r.W - 2*m
	innerH := r.H - 2*m
	if innerW <= 0 || innerH <= 0 {
		return 1
	}
	zw := innerW / (float64(fileW) * cellPx)
	zh := innerH / (float64(fileH) * cellPx)
	if zw > zh {
		return zw
	}
	return zh
}

// fileInnerBox returns the screen rectangle of a file-focused pane's
// inner area: the light-grey reading region that the textarea sits on
// (text mode) or the rendered markdown fills (rendered mode). Bounded
// horizontally by the 80-column reading width so wide panes don't
// stretch text to the full pane width. The same rect is used by the
// canvas painter, the markdown renderer, the textarea positioner, and
// the click hit-test so the user's "inside vs. outside" mental model
// is consistent across all three.
func fileInnerBox(p *pane.Pane, r paneRect) (x, y, w, h float64) {
	left, top, width, height, _ := fileTextareaBox(p, r)
	return left, top, width, height
}

// pointInFileInner reports whether (sx, sy) lies inside the
// file-focused pane's inner box. Outside the box (but still inside
// the pane) is the outer ring with grid-style mouse rules.
func pointInFileInner(p *pane.Pane, r paneRect, sx, sy float64) bool {
	ix, iy, iw, ih := fileInnerBox(p, r)
	return sx >= ix && sx < ix+iw && sy >= iy && sy < iy+ih
}

// ensureFileTextarea creates (once) the shared <textarea> overlay used
// for markdown text-mode editing. It lives in document.body and is
// positioned absolutely over the focused pane on demand. The element is
// hidden by default and only shown via refreshFileOverlay.
func (a *App) ensureFileTextarea() {
	if !a.fileTextarea.IsUndefined() && !a.fileTextarea.IsNull() {
		return
	}
	ta := a.doc.Call("createElement", "textarea")
	style := ta.Get("style")
	style.Set("position", "absolute")
	style.Set("display", "none")
	style.Set("background", colorFileInnerBg)
	style.Set("color", "#d8d9de")
	style.Set("border", "0")
	style.Set("outline", "none")
	style.Set("padding", "8px")
	style.Set("margin", "0")
	style.Set("resize", "none")
	style.Set("fontFamily", `ui-monospace, "SF Mono", Menlo, Consolas, monospace`)
	style.Set("fontSize", "14px")
	style.Set("lineHeight", "1.45")
	style.Set("zIndex", "5")
	style.Set("caretColor", "#d8d9de")
	ta.Set("spellcheck", false)
	ta.Set("autocapitalize", "off")
	ta.Set("autocorrect", "off")

	a.fileSaveCb = js.FuncOf(func(this js.Value, args []js.Value) any {
		a.fileSaveScheduled = false
		p := a.tree.FocusedPane()
		if p == nil || p.FileFocus == 0 || p.FileMode != "text" {
			return nil
		}
		a.saveFileFromTextarea(p)
		return nil
	})
	a.fileTextareaInputCb = js.FuncOf(func(this js.Value, args []js.Value) any {
		// Schedule a debounced auto-save so the canvas preview and
		// stored blob catch up to the user's edits without waiting
		// for ascent / toggle. URL update is debounced separately.
		a.scheduleFileSave()
		a.draw()
		a.scheduleURLUpdate()
		return nil
	})
	ta.Call("addEventListener", "input", a.fileTextareaInputCb)

	// Cursor moves without text changes (arrow keys, click placement,
	// page navigation) also need to refresh the URL — listen for those
	// via keyup and mouseup; input handles typed changes.
	cursorCb := js.FuncOf(func(this js.Value, args []js.Value) any {
		a.scheduleURLUpdate()
		return nil
	})
	ta.Call("addEventListener", "keyup", cursorCb)
	ta.Call("addEventListener", "mouseup", cursorCb)
	ta.Call("addEventListener", "select", cursorCb)

	a.fileTextareaScrollCb = js.FuncOf(func(this js.Value, args []js.Value) any {
		// Mirror the browser scroll position onto the focused pane so
		// SetNodeViewport on ascent persists the right value.
		p := a.tree.FocusedPane()
		if p == nil || p.FileFocus == 0 {
			return nil
		}
		p.FileScrollY = a.fileTextarea.Get("scrollTop").Float()
		return nil
	})
	ta.Call("addEventListener", "scroll", a.fileTextareaScrollCb)

	// No wheel listener: text mode uses the textarea's native scroll.
	// FileZoom is fixed for the visit, so nothing in here needs the
	// wheel event.

	// The textarea covers the whole pane in text mode, so canvas click
	// handlers never see clicks here. Forward two gestures:
	//   - Edge-zone left mousedown → file ascent (the "click any edge
	//     to leave the file" gesture).
	//   - Right mousedown → pane-management gesture, same entry point
	//     as the canvas listener so split/swap/resize work over the
	//     textarea.
	mdCb := js.FuncOf(func(this js.Value, args []js.Value) any {
		ev := args[0]
		button := ev.Get("button").Int()
		canvasRect := a.canvas.Call("getBoundingClientRect")
		sx := ev.Get("clientX").Float() - canvasRect.Get("left").Float()
		sy := ev.Get("clientY").Float() - canvasRect.Get("top").Float()
		if button == 2 {
			ev.Call("preventDefault")
			if a.transition != nil {
				return nil
			}
			p, r, ok := a.paneAtScreen(sx, sy)
			if !ok {
				return nil
			}
			a.onRightDown(p, r, sx, sy)
			return nil
		}
		if button != 0 {
			return nil
		}
		p := a.tree.FocusedPane()
		if p == nil || p.FileFocus == 0 {
			return nil
		}
		r := paneRectFor(a, p)
		if !pointInFileInner(p, r, sx, sy) {
			ev.Call("preventDefault")
			a.startFileAscent(p)
		}
		return nil
	})
	ta.Call("addEventListener", "mousedown", mdCb)

	// Mousemove and mouseup inside the textarea: forward to the
	// right-button handlers if a right-drag is in flight. Without
	// this, dragging over the textarea would freeze the gesture.
	mmCb := js.FuncOf(func(this js.Value, args []js.Value) any {
		if a.rightDrag == nil {
			return nil
		}
		ev := args[0]
		canvasRect := a.canvas.Call("getBoundingClientRect")
		sx := ev.Get("clientX").Float() - canvasRect.Get("left").Float()
		sy := ev.Get("clientY").Float() - canvasRect.Get("top").Float()
		// If the right button has been released somewhere we didn't
		// see, commit the gesture.
		if buttons := ev.Get("buttons").Int(); buttons&2 == 0 {
			a.finishRightDrag(sx, sy)
			return nil
		}
		a.onRightMove(sx, sy)
		return nil
	})
	ta.Call("addEventListener", "mousemove", mmCb)
	muCb := js.FuncOf(func(this js.Value, args []js.Value) any {
		ev := args[0]
		if a.rightDrag == nil || ev.Get("button").Int() != 2 {
			return nil
		}
		canvasRect := a.canvas.Call("getBoundingClientRect")
		sx := ev.Get("clientX").Float() - canvasRect.Get("left").Float()
		sy := ev.Get("clientY").Float() - canvasRect.Get("top").Float()
		a.finishRightDrag(sx, sy)
		return nil
	})
	ta.Call("addEventListener", "mouseup", muCb)
	// Suppress the browser's context menu over the textarea too.
	cmCb := js.FuncOf(func(this js.Value, args []js.Value) any {
		args[0].Call("preventDefault")
		return nil
	})
	ta.Call("addEventListener", "contextmenu", cmCb)

	a.doc.Get("body").Call("appendChild", ta)
	a.fileTextarea = ta
}

// refreshFileOverlay shows or hides the textarea based on whether the
// focused pane is descended into a file in text mode. Called whenever
// pane state changes (descent completes, mode toggles, ascent begins).
func (a *App) refreshFileOverlay() {
	a.ensureFileTextarea()
	ta := a.fileTextarea

	p := a.tree.FocusedPane()
	if p == nil || p.FileFocus == 0 || p.FileMode != "text" {
		ta.Get("style").Set("display", "none")
		// Move focus back to the canvas so ascent and other gestures
		// continue to work.
		a.canvas.Call("focus")
		return
	}
	r := paneRectFor(a, p)
	if r.W <= 0 || r.H <= 0 {
		ta.Get("style").Set("display", "none")
		return
	}
	style := ta.Get("style")
	left, top, width, height, fontPx := fileTextareaBox(p, r)

	style.Set("left", strconv.FormatFloat(left, 'f', 1, 64)+"px")
	style.Set("top", strconv.FormatFloat(top, 'f', 1, 64)+"px")
	style.Set("width", strconv.FormatFloat(width, 'f', 1, 64)+"px")
	style.Set("height", strconv.FormatFloat(height, 'f', 1, 64)+"px")
	if textareaNeedsClip(p, r) {
		style.Set("clipPath", textareaClipPath())
	} else {
		style.Set("clipPath", "none")
	}
	style.Set("fontSize", strconv.FormatFloat(fontPx, 'f', 1, 64)+"px")
	style.Set("display", "block")

	// Initialize textarea content from the cached blob if it isn't
	// already populated (i.e., on first show after descent).
	gid := a.gridIDForPath(p.Path)
	g, ok := a.c.Grid(gid)
	if ok {
		if file, ok := g.Nodes[p.FileFocus]; ok {
			if blob, ok := a.c.Blob(file.BlobID); ok {
				if ta.Get("value").String() == "" {
					ta.Set("value", string(blob.Data))
				}
			}
		}
	}
	// Reflect saved scroll into the textarea; on subsequent calls the
	// user's own scroll wins.
	if ta.Get("scrollTop").Float() == 0 && p.FileScrollY > 0 {
		ta.Set("scrollTop", p.FileScrollY)
	}
	ta.Call("focus")
}

// textareaClipPath cuts a square out of the bottom-right corner of the
// textarea overlay so the toggle button (drawn on the canvas) receives
// clicks instead of being shadowed by the textarea's hit region. The
// notch is sized to clear the button's circle plus a small margin.
//
// Kept in sync by hand with plusButtonInset / plusButtonRadius. Changing
// either over there should change the constant here too.
func textareaClipPath() string {
	const notch = 52 // plusButtonInset(24) + plusButtonRadius(18) + 10 px margin
	n := strconv.Itoa(notch)
	return "polygon(0 0, 100% 0, 100% calc(100% - " + n + "px), calc(100% - " + n + "px) calc(100% - " + n + "px), calc(100% - " + n + "px) 100%, 0 100%)"
}

// syncFileOverlayPosition is the lightweight version of refreshFileOverlay
// called every draw: it just repositions an already-shown textarea so it
// continues to track the focused pane through resizes and pane-tree
// edits. It does not refocus, mutate the value, or toggle visibility.
func (a *App) syncFileOverlayPosition() {
	if a.fileTextarea.IsUndefined() || a.fileTextarea.IsNull() {
		return
	}
	display := a.fileTextarea.Get("style").Get("display").String()
	if display == "none" {
		return
	}
	p := a.tree.FocusedPane()
	if p == nil || p.FileFocus == 0 || p.FileMode != "text" {
		a.fileTextarea.Get("style").Set("display", "none")
		return
	}
	r := paneRectFor(a, p)
	if r.W <= 0 || r.H <= 0 {
		return
	}
	left, top, width, height, fontPx := fileTextareaBox(p, r)
	style := a.fileTextarea.Get("style")
	style.Set("left", strconv.FormatFloat(left, 'f', 1, 64)+"px")
	style.Set("top", strconv.FormatFloat(top, 'f', 1, 64)+"px")
	style.Set("width", strconv.FormatFloat(width, 'f', 1, 64)+"px")
	style.Set("height", strconv.FormatFloat(height, 'f', 1, 64)+"px")
	style.Set("fontSize", strconv.FormatFloat(fontPx, 'f', 1, 64)+"px")
	if textareaNeedsClip(p, r) {
		style.Set("clipPath", textareaClipPath())
	} else {
		style.Set("clipPath", "none")
	}
}

// fileTextareaBox returns the textarea overlay's screen rectangle and
// font size for pane p with rect r. Inset by fileMargin so a margin of
// outer-ring space surrounds the textarea, and width is capped at the
// 80-column reading width so long files don't stretch full-pane.
func fileTextareaBox(p *pane.Pane, r paneRect) (left, top, width, height, fontPx float64) {
	zoom := p.FileZoom
	if zoom <= 0 {
		zoom = 1.0
	}
	fontPx = 14.0 * zoom
	if fontPx < 8 {
		fontPx = 8
	}
	if fontPx > 60 {
		fontPx = 60
	}
	m := fileMargin(r)
	innerX := r.X + m
	innerY := r.Y + m
	innerW := r.W - 2*m
	innerH := r.H - 2*m
	if innerW < 0 {
		innerW = 0
	}
	if innerH < 0 {
		innerH = 0
	}
	const maxCols = 80
	colWidth := float64(maxCols)*fontPx*0.6 + 32
	width = colWidth
	if width > innerW {
		width = innerW
	}
	left = innerX + (innerW-width)/2
	top = innerY
	height = innerH
	return
}

// textareaNeedsClip reports whether the textarea's bottom-right
// corner overlaps the file toggle button on this pane. When false
// (typical case: pane is wide enough for the toggle button to live
// in the outer ring), the clipPath notch is unnecessary and would
// cause a visible cut.
func textareaNeedsClip(p *pane.Pane, r paneRect) bool {
	bx, by := plusButtonCenter(r)
	left, top, width, height, _ := fileTextareaBox(p, r)
	right := left + width
	bottom := top + height
	return bx-plusButtonRadius < right && by-plusButtonRadius < bottom
}

// onToggleFileMode flips the focused pane between text and rendered
// modes. Going text→rendered saves the current buffer first; going
// rendered→text just shows the textarea (the buffer is the cached blob
// from the last save).
func (a *App) onToggleFileMode(p *pane.Pane) {
	if p.FileFocus == 0 {
		return
	}
	if p.FileMode == "text" {
		// Save before switching to rendered.
		a.saveFileFromTextarea(p)
		p.FileMode = "rendered"
	} else {
		p.FileMode = "text"
		// Reset textarea contents next time refreshFileOverlay is called
		// so it picks up the freshest cached blob.
		if !a.fileTextarea.IsUndefined() && !a.fileTextarea.IsNull() {
			a.fileTextarea.Set("value", "")
		}
	}
	a.fileLastMode[p.FileFocus] = p.FileMode
	a.refreshFileOverlay()
	a.draw()
	a.scheduleURLUpdate()
}

// saveFileFromTextarea posts the textarea's value as the file's new
// content. The cached blob is updated under the file's *current*
// BlobID synchronously, so the immediate post-toggle render uses the
// user's typed content rather than the stale (pre-edit) blob. The
// RPC then runs async; on completion the cache is also updated under
// the new (content-hashed) BlobID, and the SSE NodeChanged event
// repoints the cached node at it.
func (a *App) saveFileFromTextarea(p *pane.Pane) {
	if a.fileTextarea.IsUndefined() || a.fileTextarea.IsNull() {
		return
	}
	buf := a.fileTextarea.Get("value").String()
	gid := a.gridIDForPath(p.Path)
	g, ok := a.c.Grid(gid)
	if !ok {
		return
	}
	file, ok := g.Nodes[p.FileFocus]
	if !ok {
		return
	}
	// Bridge: replace the cached blob under the existing BlobID so
	// renders that read the cached node's blob_id between now and the
	// RPC round-trip see the user's content.
	if file.BlobID != 0 {
		a.c.PutBlob(file.BlobID, []byte(buf), "text/markdown")
	}
	r := paneRectFor(a, p)
	pscreen := dragdrop.Pane{
		ScreenX: r.X, ScreenY: r.Y, ScreenW: r.W, ScreenH: r.H,
		Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
	}
	view := a.paneViewRect(p, pscreen)
	go func() {
		req := rpc.UpdateFileContentRequest{
			Path: rpc.Path{WellIDs: p.Path}, ViewRect: view,
			NodeID: file.ID, Data: []byte(buf),
		}
		var resp rpc.NodeResponse
		if _, err := postJSON("/rpc/UpdateFileContent", req, &resp); err == nil {
			a.c.PutBlob(resp.Node.BlobID, []byte(buf), "text/markdown")
		}
	}()
}
