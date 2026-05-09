//go:build js && wasm

package main

import (
	"strconv"
	"syscall/js"

	"github.com/josephburnett/ascent/client/dragdrop"
	"github.com/josephburnett/ascent/client/pane"
	"github.com/josephburnett/ascent/internal/rpc"
)

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
	style.Set("background", "transparent")
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

	a.fileTextareaInputCb = js.FuncOf(func(this js.Value, args []js.Value) any {
		// Just trigger a redraw so the canvas-side preview (if any) stays
		// in sync. We don't push every keystroke to the server — that
		// happens on toggle-to-rendered or ascend.
		a.draw()
		return nil
	})
	ta.Call("addEventListener", "input", a.fileTextareaInputCb)

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

	// The textarea covers the whole pane while in text mode, so the
	// canvas's contextmenu handler never sees right-clicks here. Forward
	// edge-zone right-clicks to ascent so the user retains the
	// "right-click any edge to ascend" gesture.
	cmCb := js.FuncOf(func(this js.Value, args []js.Value) any {
		args[0].Call("preventDefault")
		ev := args[0]
		canvasRect := a.canvas.Call("getBoundingClientRect")
		sx := ev.Get("clientX").Float() - canvasRect.Get("left").Float()
		sy := ev.Get("clientY").Float() - canvasRect.Get("top").Float()
		p := a.tree.FocusedPane()
		if p == nil || p.FileFocus == 0 {
			return nil
		}
		r := paneRectFor(a, p)
		ps := dragdrop.Pane{
			ScreenX: r.X, ScreenY: r.Y, ScreenW: r.W, ScreenH: r.H,
			Cx: p.Cx, Cy: p.Cy, Zoom: p.Zoom, CellPx: cellPx,
		}
		if dragdrop.IsInEdgeZone(ps, sx, sy, dragdrop.EdgeBand(ps)) {
			a.startFileAscent(p)
		}
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
	style.Set("left", strconv.FormatFloat(r.X, 'f', 1, 64)+"px")
	style.Set("top", strconv.FormatFloat(r.Y, 'f', 1, 64)+"px")
	style.Set("width", strconv.FormatFloat(r.W, 'f', 1, 64)+"px")
	style.Set("height", strconv.FormatFloat(r.H, 'f', 1, 64)+"px")
	// Scale font-size with the pane's zoom so an editing session at high
	// zoom shows readable text without snapping to a different size when
	// the descent animation lands.
	scale := p.Zoom
	if scale < 1 {
		scale = 1
	}
	fontPx := 14.0 * scale
	if fontPx > 28 {
		fontPx = 28
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
	style := a.fileTextarea.Get("style")
	style.Set("left", strconv.FormatFloat(r.X, 'f', 1, 64)+"px")
	style.Set("top", strconv.FormatFloat(r.Y, 'f', 1, 64)+"px")
	style.Set("width", strconv.FormatFloat(r.W, 'f', 1, 64)+"px")
	style.Set("height", strconv.FormatFloat(r.H, 'f', 1, 64)+"px")
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
	a.saveTreeToLocalStorage()
}

// saveFileFromTextarea posts the textarea's value as the file's new
// content. Async; we don't block the UI on the round-trip. On success
// the cached blob is updated so the rendered view reflects the new
// content immediately.
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
