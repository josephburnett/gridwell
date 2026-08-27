//go:build js && wasm

package main

import (
	"strconv"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/markdown"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panebox"
	"github.com/josephburnett/gridwell/client/textedit"
	"github.com/josephburnett/gridwell/client/wsbar"
)

// textSaveDebounceMs is the delay between the first keystroke since
// the last save and the next save fire. Continuous typing therefore
// saves at most once per this interval; a typing pause longer than
// this resolves with one final save shortly after the user stops.
const textSaveDebounceMs = 600

// pxf formats a logical pixel value as a CSS "<n>px" string (1 decimal) — the
// one place the overlay code turns a float coordinate into a style value.
func pxf(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) + "px" }

// setBoundsPx writes an absolutely-positioned element's left/top/width/height
// from logical pixels. Used wherever a DOM overlay (textarea, shell container)
// is positioned over a pane rect.
func setBoundsPx(style js.Value, left, top, width, height float64) {
	style.Set("left", pxf(left))
	style.Set("top", pxf(top))
	style.Set("width", pxf(width))
	style.Set("height", pxf(height))
}

// scheduleFileSave queues a debounced save of the focused pane's
// textarea contents. Cheap to call from every keystroke — no-op if a
// save is already pending.
func (a *App) scheduleFileSave() {
	if a.sched.textSaveScheduled {
		return
	}
	a.sched.textSaveScheduled = true
	js.Global().Call("setTimeout", a.sched.textSaveCb, textSaveDebounceMs)
}

// textFitZoom returns the parent zoom at which the file tile's
// footprint (W × H cells) exactly fits inside the inner-box dimensions
// (textarea region) of pane rect r — the smaller inner-box dim binds.
//
// Uses zoomtrans.Fit (min of dim ratios), not Overtake (max). The
// distinction matters when the file's footprint aspect ≠ the inner-box
// aspect (e.g., a 1×1 file in a landscape pane): the binding dim is
// what limits user content in live mode, so calibrating ViewZoom
// against it makes the preview text fill the file cell at the same
// fraction as live text fills the inner-box. Thin adapter over
// panebox.FitZoom bundling the wasm renderer's constants.
func textFitZoom(r pane.Rect, fileW, fileH int64) float64 {
	return panebox.FitZoom(r, fileW, fileH, textSideInset, cellPx)
}

// textInnerBox returns the screen rectangle of a file-focused pane's
// inner area: the light-grey reading region that the textarea sits on
// (text mode) or the rendered markdown fills (rendered mode). The same
// rect is used by the canvas painter, the markdown renderer, the
// textarea positioner, and the click hit-test so the user's "inside
// vs. outside" mental model is consistent across all three.
//
// URL tiles use the full pane content area (paneContentBox) instead
// of this narrower textarea-shaped box — see drawURLTileInPane and
// the mouse/wheel handlers' isURLDescent branches.
func textInnerBox(r pane.Rect) (x, y, w, h float64) {
	b := panebox.InnerBox(r, textSideInset)
	return b.X, b.Y, b.W, b.H
}

// barAwarePaneRect is pane p's rect with the focused pane's bar band
// carved out (issue #220) — the rect every overlay/native surface sizes
// from, so none can occlude the band.
func (a *App) barAwarePaneRect(p *pane.Pane) pane.Rect {
	return panebox.BarInset(paneRectFor(a, p), wsbar.RowH)
}

// liveContentBox returns the rectangle a live surface (url/page view,
// shell overlay) occupies in pane rect r, and the rectangle its parked
// fallback frame is drawn into: the pane minus the bar band minus the
// outline — panebox.LiveContentBox with this renderer's constants. Web
// content fills it edge-to-edge (pages have their own layout); ascent
// is via the Escape key. There is deliberately no un-inset variant.
func liveContentBox(r pane.Rect) (x, y, w, h float64) {
	b := panebox.LiveContentBox(r, wsbar.RowH, paneBorderPx)
	return b.X, b.Y, b.W, b.H
}

// pointInLiveContent / pointInFileInner are thin adapters over the
// panebox hit-tests, supplying the wasm renderer's constants.
func pointInLiveContent(r pane.Rect, sx, sy float64) bool {
	return panebox.PointInLiveContent(r, wsbar.RowH, paneBorderPx, sx, sy)
}

func pointInFileInner(r pane.Rect, sx, sy float64) bool {
	return panebox.PointInInner(r, textSideInset, sx, sy)
}

// ensureFileTextarea creates (once) the shared <textarea> overlay used
// for markdown text-mode editing. It lives in document.body and is
// positioned absolutely over the focused pane on demand. The element is
// hidden by default and only shown via refreshFileOverlay.
func (a *App) ensureFileTextarea() {
	if !a.textTextarea.IsUndefined() && !a.textTextarea.IsNull() {
		return
	}
	ta := a.doc.Call("createElement", "textarea")
	ta.Set("id", "gw-text-editor") // stable hook for e2e (wrap parity, #216)
	style := ta.Get("style")
	style.Set("position", "absolute")
	style.Set("display", "none")
	style.Set("background", colorFileInnerBg)
	style.Set("color", "#d8d9de")
	style.Set("border", "0")
	style.Set("outline", "none")
	// border-box so the padding fits inside the width/height
	// textTextareaBox returns. Without this, content-box would add the
	// padding to each dimension and the textarea would overhang the
	// pane's right and bottom border strokes.
	style.Set("boxSizing", "border-box")
	// Metrics mirror the canvas painter (drawMarkdownText) exactly so the
	// raw text doesn't reflow when focus enters/leaves the pane: same font
	// size (codePx), same line-height (rawTextLineHeight), same symmetric
	// inset (pad). The painter replicates this line box's baseline placement
	// from real font metrics rather than the other way round, so the inset
	// here stays plain and uniform.
	mst := defaultMarkdownStyle()
	style.Set("padding", strconv.FormatFloat(mst.pad, 'f', 3, 64)+"px")
	style.Set("margin", "0")
	style.Set("resize", "none")
	style.Set("fontFamily", mst.monospace)
	style.Set("fontSize", pxf(mst.codePx))
	style.Set("lineHeight", strconv.FormatFloat(rawTextLineHeight, 'f', 2, 64))
	style.Set("zIndex", "5")
	style.Set("caretColor", "#d8d9de")
	ta.Set("spellcheck", false)
	ta.Set("autocapitalize", "off")
	ta.Set("autocorrect", "off")

	a.sched.textSaveCb = js.FuncOf(func(this js.Value, args []js.Value) any {
		a.sched.textSaveScheduled = false
		// Sweep every dirty content entry, whoever holds focus now. The old
		// fire-time guards (focused pane, mode, singleton binding) existed
		// because the save read the DOM and had to prove the DOM still
		// belonged to the tile; a sweep over tile-keyed entries needs none of
		// that, and can't strand an edit whose pane has moved on.
		a.flushDirtyText()
		return nil
	})
	a.textTextareaInputCb = js.FuncOf(func(this js.Value, args []js.Value) any {
		// Mirror the keystroke into the content store under the tile the
		// textarea is BOUND to — the one owner of unsaved text. The DOM value
		// is a view; nothing ever persists from it directly. Then arm the
		// debounced sweep. URL update is debounced separately.
		a.textareaReady = true
		if a.lastTextareaTileID == "" {
			// Typing into an unbound textarea has no tile to belong to. This
			// state should be unreachable (an unbound textarea is hidden);
			// surface it rather than let the typing vanish silently.
			a.reportErr(errsurface.Error, "textedit",
				"typing arrived with no bound tile — this edit cannot be saved")
			return nil
		}
		// Keyed by contentKey: a leaf link's edits accumulate under its
		// TARGET's id — the one shared content fact (see text_flush.go).
		a.c.PutEditedContent(a.contentKey(a.lastTextareaTileID), []byte(a.textTextarea.Get("value").String()))
		a.scheduleFileSave()
		a.draw()
		a.scheduleURLUpdate()
		return nil
	})
	ta.Call("addEventListener", "input", a.textTextareaInputCb)

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

	a.textTextareaScrollCb = js.FuncOf(func(this js.Value, args []js.Value) any {
		// Mirror the browser scroll position onto the focused pane so
		// SetTextView on ascent persists the right value — but only when the
		// textarea is actually BOUND to the focused pane's tile. Without the
		// binding check this was the framing twin of the cross-tile stomp:
		// in a stale-binding window, tile A's scroll offset landed on tile
		// B's pane and persisted as B's text_y.
		p := a.tree.FocusedPane()
		if p == nil || p.TextFocus == "" || p.TextFocus != a.lastTextareaTileID {
			return nil
		}
		p.TextScrollY = a.textTextarea.Get("scrollTop").Float()
		return nil
	})
	ta.Call("addEventListener", "scroll", a.textTextareaScrollCb)

	// No wheel listener: text mode uses the textarea's native scroll.
	// TextZoom is fixed for the visit, so nothing in here needs the
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
		if button == 1 {
			// Middle-click ascends, same as on the canvas. The textarea
			// covers the whole pane in text mode, so the canvas listener
			// never sees this press — forward it here.
			ev.Call("preventDefault")
			if a.transition != nil {
				return nil
			}
			if p := a.tree.FocusedPane(); p != nil {
				a.menu.Close()
				a.ascendPane(p)
			}
			return nil
		}
		if button != 0 {
			return nil
		}
		p := a.tree.FocusedPane()
		if p == nil || p.TextFocus == "" {
			return nil
		}
		r := paneRectFor(a, p)
		if !pointInFileInner(r, sx, sy) {
			ev.Call("preventDefault")
			a.startTextAscent(p)
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

	// Multi-finger touches forward into the touch gesture machine (two-finger
	// tap = ascend, pinch = wheel) — the touch analogue of the mouse
	// forwarding above. Single-finger touches keep native textarea behavior
	// (caret, selection, the OS keyboard). See installTextareaTouch.
	a.installTextareaTouch(ta)

	a.doc.Get("body").Call("appendChild", ta)
	a.textTextarea = ta
}

// ensureFileToggle creates (once) the floating rendered/raw toggle
// button used during a markdown descent. A DOM element layered above the
// textarea (zIndex 6 > textarea 5) so the text content can fill the pane.
func (a *App) ensureFileToggle() {
	if !a.textToggleBtn.IsUndefined() && !a.textToggleBtn.IsNull() {
		return
	}
	btn := a.doc.Call("createElement", "div")
	btn.Set("id", "gw-text-toggle")
	style := btn.Get("style")
	style.Set("position", "absolute")
	style.Set("display", "none")
	style.Set("boxSizing", "border-box")
	style.Set("width", strconv.Itoa(2*plusButtonRadius)+"px")
	style.Set("height", strconv.Itoa(2*plusButtonRadius)+"px")
	style.Set("borderRadius", "50%")
	// background/color are NOT set here: refreshFileToggle derives them from
	// barTheme on every refresh (issue #227 — a create-time color was a
	// second, frozen copy of the theme fact).
	style.Set("border", "1px solid #dff4f4")
	style.Set("cursor", "pointer")
	style.Set("alignItems", "center")
	style.Set("justifyContent", "center")
	style.Set("zIndex", "6")
	style.Set("userSelect", "none")
	style.Set("fontSize", "18px")
	btn.Set("textContent", "a")

	a.textToggleCb = js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		ev := args[0]
		ev.Call("preventDefault")
		ev.Call("stopPropagation")
		p := a.tree.FocusedPane()
		if p == nil || p.TextFocus == "" {
			return nil
		}
		// LEFT-click toggles rendered/raw; right-click does nothing — the
		// ascent gesture is clicking the previous crumb (issue #222).
		if ev.Get("button").Int() != 0 {
			return nil
		}
		a.onToggleFileMode(p)
		return nil
	})
	btn.Call("addEventListener", "mousedown", a.textToggleCb)
	// Suppress the browser context menu so a right-click on the toggle
	// stays inert (#222) instead of popping a menu.
	btn.Call("addEventListener", "contextmenu", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) > 0 {
			args[0].Call("preventDefault")
		}
		return nil
	}))
	// Touch: the shared translation routes a tap here as a left mousedown
	// (toggle) — without this the button was mouse-only (issue #191).
	a.installOverlayTouch(btn, nil)
	a.doc.Get("body").Call("appendChild", btn)
	a.textToggleBtn = btn
}

// refreshFileToggle positions/styles the floating toggle for a markdown
// descent (any mode) and hides it otherwise. URL tiles use a canvas back
// button instead, so they're excluded.
func (a *App) refreshFileToggle() {
	a.ensureFileToggle()
	style := a.textToggleBtn.Get("style")
	hide := func() { style.Set("display", "none") }

	p := a.tree.FocusedPane()
	if p == nil || p.TextFocus == "" || a.isURLDescent(p) {
		hide()
		return
	}
	gid := a.gridIDForPane(p)
	g, ok := a.c.Grid(gid)
	if !ok {
		hide()
		return
	}
	file, ok := g.Tiles[p.TextFocus]
	if !ok || file.Kind != rpc.KindText {
		hide()
		return
	}
	if !textToggleVisible(&file, a.tileReadOnly(&file)) {
		hide()
		return
	}
	r := paneRectFor(a, p)
	if r.W <= 0 || r.H <= 0 {
		hide()
		return
	}
	cx, cy := a.plusButtonCenter()
	style.Set("left", pxf(cx-plusButtonRadius))
	style.Set("top", pxf(cy-plusButtonRadius))
	// The family shades, same as the canvas slot buttons (issue #223/#227):
	// saturated hue for the face, the dark band shade for the glyph.
	band, button := a.barTheme()
	style.Set("background", button)
	style.Set("color", band)
	// Glyph hints at the TARGET mode: an italic serif "a" means clicking
	// renders; a monospace "a" means clicking edits the source.
	if p.TextMode == rpc.TextModeRendered {
		style.Set("fontFamily", `ui-monospace, "SF Mono", Menlo, Consolas, monospace`)
		style.Set("fontStyle", "normal")
	} else {
		style.Set("fontFamily", `ui-serif, "Times New Roman", Georgia, serif`)
		style.Set("fontStyle", "italic")
	}
	style.Set("display", "flex")
}

// refreshFileOverlay shows or hides the textarea based on whether the
// focused pane is descended into a file in text mode. Called whenever
// pane state changes (descent completes, mode toggles, ascent begins).
func (a *App) refreshFileOverlay() {
	a.refreshFileToggle()
	a.refreshRenderedOverlay()
	a.ensureFileTextarea()
	ta := a.textTextarea

	p := a.tree.FocusedPane()
	if p == nil || p.TextFocus == "" || p.TextMode != rpc.TextModeText {
		ta.Get("style").Set("display", "none")
		// Move focus back to the canvas so ascent and other gestures
		// continue to work.
		a.focusCanvas()
		return
	}
	// Source-backed text tiles are read-only — never show the textarea
	// even if a stale TextMode says "text". (The mode is server-stored
	// and can outlive the source-key being set, so this is the only
	// place we can enforce the invariant client-side.)
	if g, ok := a.c.Grid(a.gridIDForPane(p)); ok {
		if file, ok := g.Tiles[p.TextFocus]; ok && a.tileReadOnly(&file) {
			ta.Get("style").Set("display", "none")
			a.focusCanvas()
			return
		}
	}
	r := a.barAwarePaneRect(p)
	if r.W <= 0 || r.H <= 0 {
		ta.Get("style").Set("display", "none")
		return
	}
	style := ta.Get("style")
	left, top, width, height, fontPx := a.textTextareaBox(p, r)

	setBoundsPx(style, left, top, width, height)
	style.Set("clipPath", "none")
	style.Set("fontSize", pxf(fontPx))
	style.Set("display", "block")

	// Sync the textarea singleton to the focused tile. The decision
	// lives in textedit.DecideTextareaSync so it's natively
	// testable — the wasm side just gathers inputs from cache + DOM
	// and applies the result. Critically, on a tile switch we clear
	// immediately even when the blob hasn't loaded yet, so the
	// previous tile's buffer doesn't appear as the new tile's
	// "default" content. The blob-fetch onComplete fires
	// refreshFileOverlay again with the actual content.
	gid := a.gridIDForPane(p)
	_, pendingEdit := a.c.DirtyContent(a.contentKey(a.lastTextareaTileID))
	in := textedit.TextareaSyncInput{
		FocusedTileID: p.TextFocus,
		LastTileID:    a.lastTextareaTileID,
		CurrentValue:  ta.Get("value").String(),
		PendingEdit:   pendingEdit,
	}
	if g, ok := a.c.Grid(gid); ok {
		if file, ok := g.Tiles[p.TextFocus]; ok {
			if body, ok := a.tileBody(&file); ok {
				in.BlobCached = true
				in.BlobContent = string(body)
			}
		}
	}
	// A rebind rescues nothing and discards nothing: the old tile's typing
	// lives in its own content-store entry, and the dirty sweep posts it no
	// matter which pane (if any) still shows it. The old "flush old first /
	// discard when the pane is gone" seam is unrepresentable now.
	dec := textedit.DecideTextareaSync(in)
	if dec.SetValue {
		ta.Set("value", dec.Value)
		// Track whether the textarea now has content for textedit.CanvasHiddenByOverlay:
		// true = overlay covers this pane with actual content (canvas should hide).
		// false = textarea cleared on tile switch / blob not yet arrived (canvas must
		// keep painting — the loading-race blank, issue #35 mechanism B).
		a.textareaReady = dec.Value != ""
	}
	a.lastTextareaTileID = dec.NewLastTileID
	// Reflect saved scroll into the textarea; on subsequent calls the
	// user's own scroll wins.
	if ta.Get("scrollTop").Float() == 0 && p.TextScrollY > 0 {
		ta.Set("scrollTop", p.TextScrollY)
	}
	ta.Call("focus")
}

// focusCanvas returns keyboard focus to the canvas — UNLESS the inline
// rename input is open. This runs on every async overlay refresh (a
// content fetch landing, a TileChanged event), and unconditional
// canvas.focus() here yanked focus out of the rename input moments after
// it opened — typing landed on the canvas, and (now that blur commits)
// it would also close the input mid-thought.
func (a *App) focusCanvas() {
	if a.renameEditing {
		return
	}
	a.canvas.Call("focus")
}

// syncTextOverlayPosition is the lightweight version of refreshFileOverlay
// called every draw: it just repositions an already-shown textarea so it
// continues to track the focused pane through resizes and pane-tree
// edits. It does not refocus, mutate the value, or toggle visibility.
func (a *App) syncTextOverlayPosition() {
	a.refreshFileToggle()
	if a.textTextarea.IsUndefined() || a.textTextarea.IsNull() {
		return
	}
	display := a.textTextarea.Get("style").Get("display").String()
	if display == "none" {
		return
	}
	p := a.tree.FocusedPane()
	if p == nil || p.TextFocus == "" || p.TextMode != rpc.TextModeText {
		a.textTextarea.Get("style").Set("display", "none")
		return
	}
	r := a.barAwarePaneRect(p)
	if r.W <= 0 || r.H <= 0 {
		return
	}
	left, top, width, height, fontPx := a.textTextareaBox(p, r)
	style := a.textTextarea.Get("style")
	setBoundsPx(style, left, top, width, height)
	style.Set("fontSize", pxf(fontPx))
	style.Set("clipPath", "none")
}

// textTextareaBox returns the textarea overlay's screen rectangle and
// font size for pane p with rect r. Adapter over panebox.TextareaBox
// supplying the wasm renderer's fixed-scale constants.
func (a *App) textTextareaBox(p *pane.Pane, r pane.Rect) (left, top, width, height, fontPx float64) {
	// Font size = the canvas painter's codePx at the pane's live scale
	// (base × the tile's content zoom, issue #82), so focused (textarea) and
	// blurred (canvas) raw text are the same size. See drawMarkdownText.
	b, fp := panebox.TextareaBox(r, textSideInset, defaultMarkdownStyle().codePx, a.textScaleFor(p))
	return b.X, b.Y, b.W, b.H, fp
}

// textSideInset is the gap between the pane edge and the text content —
// a small reading margin so glyphs don't touch the frame. Kept fixed
// (independent of the now-1px paneBorderPx) so thinning the colored
// border didn't cram text against the edge. The rendered/raw toggle is a
// DOM overlay button (refreshFileToggle), so no strip is reserved for it.
const textSideInset = 6.0

// onToggleFileMode flips the focused pane between text and rendered
// modes. Going text→rendered saves the current buffer first; going
// rendered→text just shows the textarea (the buffer is the cached blob
// from the last save).
func (a *App) onToggleFileMode(p *pane.Pane) {
	if p.TextFocus == "" {
		return
	}
	// A read-only NON-renderable tile has no mode to flip to; a renderable
	// host file flips rendered/raw source (issue #236) — the textarea
	// guard in refreshFileOverlay keeps raw mode caret-free either way.
	if g, ok := a.c.Grid(a.gridIDForPane(p)); ok {
		if file, ok := g.Tiles[p.TextFocus]; ok && !textToggleVisible(&file, a.tileReadOnly(&file)) {
			return
		}
	}
	if p.TextMode == rpc.TextModeText {
		// Flush any pending typing before switching to rendered — from the
		// content store (the keystrokes already live there), never the DOM.
		a.flushTileContent(p.TextFocus)
		p.TextMode = rpc.TextModeRendered
	} else {
		p.TextMode = rpc.TextModeText
		// Reset textarea contents next time refreshFileOverlay is called
		// so it picks up the freshest cached blob.
		if !a.textTextarea.IsUndefined() && !a.textTextarea.IsNull() {
			a.textTextarea.Set("value", "")
			a.textareaReady = false // cleared; refreshFileOverlay re-seeds it
		}
	}
	// The mode is persisted to the tile on ascent (saveTextBeforeAscent).
	// While descended, the focused pane's live TextMode drives the preview.
	a.refreshFileOverlay()
	a.draw()
	a.scheduleURLUpdate()
}

// saveTextFromTextarea is GONE. Text bytes reach the server through exactly
// one door — client/wasm/text_flush.go — which reads the content store by
// tile id and never the DOM. See that file for why (the cross-tile stomp).

// textToggleVisible decides whether the rendered/raw toggle exists for a
// text tile. The owning plugin's text_presentation is the one authority
// when declared (decision 2026-08-13): "both" keeps the flip (rendered vs
// raw source — the textarea guard still keeps raw read-only), "plain" and
// "rendered" are single-presentation. Undeclared tiles keep the legacy
// rule: writable docs always toggle; a read-only tile toggles only when
// its name is renderable (fs .md/.org — issue #236), because a metadata
// summary has nothing to flip.
func textToggleVisible(file *rpc.Tile, readOnly bool) bool {
	switch file.TextPresentation {
	case rpc.TextPresentationBoth:
		return true
	case rpc.TextPresentationPlain, rpc.TextPresentationRendered:
		return false
	}
	return !readOnly || markdown.Renderable(file.AltText)
}
