//go:build js && wasm

package main

import (
	"strconv"
	"syscall/js"

	embedpkg "github.com/josephburnett/gridwell/client/embed"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panebox"
	"github.com/josephburnett/gridwell/client/textedit"
	"github.com/josephburnett/gridwell/internal/rpc"
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
// fraction as live text fills the inner-box.
// textFitZoom is a thin adapter over panebox.FitZoom that
// bundles the wasm-renderer's constants (textSideInset + cellPx).
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
func textInnerBox(_ *pane.Pane, r pane.Rect) (x, y, w, h float64) {
	b := panebox.InnerBox(r, textSideInset)
	return b.X, b.Y, b.W, b.H
}

// paneContentBox returns the rectangle a URL tile renders into when
// descended: the full pane minus the pane outline thickness. URL tiles
// fill the pane edge-to-edge — the wide textarea-style margin used by
// markdown files makes no sense here, since web pages have their own
// internal layout and want all the pixels they can get. Ascent out of
// a URL tile is via the Escape key.
func paneContentBox(r pane.Rect) (x, y, w, h float64) {
	b := panebox.ContentBox(r, paneBorderPx)
	return b.X, b.Y, b.W, b.H
}

// pointInPaneContent / pointInFileInner are thin adapters over the panebox
// hit-tests, supplying the wasm renderer's border / inset constants.
func pointInPaneContent(r pane.Rect, sx, sy float64) bool {
	return panebox.PointInContent(r, paneBorderPx, sx, sy)
}

func pointInFileInner(_ *pane.Pane, r pane.Rect, sx, sy float64) bool {
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
		p := a.tree.FocusedPane()
		// The fire-time guard (focused text-tile pane, in raw-text mode,
		// with the textarea singleton still bound to THIS tile) is the pure
		// textedit.ShouldDebouncedSaveFire — see there for why the
		// last-bound check matters (the cross-tile stale-buffer regression).
		var (
			textFocus string
			textMode  bool
		)
		if p != nil {
			textFocus = p.TextFocus
			textMode = p.TextMode == rpc.TextModeText
		}
		// Rendered-mode edits don't flow through the textarea; persist them
		// from the optimistically-updated cache instead. Only when the tile
		// actually has a pending edit (mdDirty) — a timer that fires after the
		// user navigated to a different, unedited rendered tile must not re-post
		// its unchanged content.
		if p != nil && p.TextMode == rpc.TextModeRendered {
			if pl, ok := a.localIf(p.ID); ok && pl.Dirty {
				a.saveTextFromCache(p)
			}
			return nil
		}
		if !textedit.ShouldDebouncedSaveFire(p != nil, textFocus, textMode, a.lastTextareaTileID) {
			// Declined (focus moved away mid-debounce). textareaDirty stays
			// true: the rebind flush in refreshFileOverlay posts the edit.
			return nil
		}
		if !a.textareaDirty {
			// Already flushed (rebind or mode toggle) — don't re-post the
			// buffer and bump the tile's version for nothing.
			return nil
		}
		a.saveTextFromTextarea(p)
		return nil
	})
	a.textTextareaInputCb = js.FuncOf(func(this js.Value, args []js.Value) any {
		// Schedule a debounced auto-save so the canvas preview and
		// stored blob catch up to the user's edits without waiting
		// for ascent / toggle. URL update is debounced separately.
		// The user has typed something → the textarea definitely has
		// content now; mark it ready so canvas hiding stays correct, and
		// dirty so no rebind can destroy the edit before it is posted.
		a.textareaReady = true
		a.textareaDirty = true
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
		// SetTextView on ascent persists the right value.
		p := a.tree.FocusedPane()
		if p == nil || p.TextFocus == "" {
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
		if !pointInFileInner(p, r, sx, sy) {
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
	style := btn.Get("style")
	style.Set("position", "absolute")
	style.Set("display", "none")
	style.Set("boxSizing", "border-box")
	style.Set("width", strconv.Itoa(2*plusButtonRadius)+"px")
	style.Set("height", strconv.Itoa(2*plusButtonRadius)+"px")
	style.Set("borderRadius", "50%")
	style.Set("background", colorPlusBg)
	style.Set("border", "1px solid "+colorPaneBorder)
	style.Set("color", colorPlusFg)
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
		// Right-click on the round button ascends, like every other corner
		// circle. Left-click toggles rendered/raw.
		if ev.Get("button").Int() == 2 {
			a.ascendPane(p)
			return nil
		}
		a.onToggleFileMode(p)
		return nil
	})
	btn.Call("addEventListener", "mousedown", a.textToggleCb)
	// Suppress the browser context menu so right-click reads purely as the
	// ascend gesture above.
	btn.Call("addEventListener", "contextmenu", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) > 0 {
			args[0].Call("preventDefault")
		}
		return nil
	}))
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
	if a.tileReadOnly(&file) {
		// Source-backed text tiles have no editable mode to toggle to —
		// the body is reconciler output, not user content. Hiding the
		// glyph keeps the read-only contract visible at a glance.
		hide()
		return
	}
	r := paneRectFor(a, p)
	if r.W <= 0 || r.H <= 0 {
		hide()
		return
	}
	cx, cy := plusButtonCenter(p, r)
	style.Set("left", pxf(cx-plusButtonRadius))
	style.Set("top", pxf(cy-plusButtonRadius))
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
	a.ensureFileTextarea()
	ta := a.textTextarea

	p := a.tree.FocusedPane()
	if p == nil || p.TextFocus == "" || p.TextMode != rpc.TextModeText {
		ta.Get("style").Set("display", "none")
		// Move focus back to the canvas so ascent and other gestures
		// continue to work.
		a.canvas.Call("focus")
		return
	}
	// Source-backed text tiles are read-only — never show the textarea
	// even if a stale TextMode says "text". (The mode is server-stored
	// and can outlive the source-key being set, so this is the only
	// place we can enforce the invariant client-side.)
	if g, ok := a.c.Grid(a.gridIDForPane(p)); ok {
		if file, ok := g.Tiles[p.TextFocus]; ok && a.tileReadOnly(&file) {
			ta.Get("style").Set("display", "none")
			a.canvas.Call("focus")
			return
		}
	}
	r := paneRectFor(a, p)
	if r.W <= 0 || r.H <= 0 {
		ta.Get("style").Set("display", "none")
		return
	}
	style := ta.Get("style")
	left, top, width, height, fontPx := textTextareaBox(p, r)

	setBoundsPx(style, left, top, width, height)
	style.Set("clipPath", "none")
	style.Set("fontSize", pxf(fontPx))
	style.Set("display", "block")

	// Sync the textarea singleton to the focused tile. The decision
	// lives in client/embed.DecideTextareaSync so it's natively
	// testable — the wasm side just gathers inputs from cache + DOM
	// and applies the result. Critically, on a tile switch we clear
	// immediately even when the blob hasn't loaded yet, so the
	// previous tile's buffer doesn't appear as the new tile's
	// "default" content. The blob-fetch onComplete fires
	// refreshFileOverlay again with the actual content.
	gid := a.gridIDForPane(p)
	in := embedpkg.TextareaSyncInput{
		FocusedTileID: p.TextFocus,
		LastTileID:    a.lastTextareaTileID,
		CurrentValue:  ta.Get("value").String(),
		PendingEdit:   a.textareaDirty,
	}
	if g, ok := a.c.Grid(gid); ok {
		if file, ok := g.Tiles[p.TextFocus]; ok {
			if body, ok := a.tileBody(&file); ok {
				in.BlobCached = true
				in.BlobContent = string(body)
			}
		}
	}
	dec := embedpkg.DecideTextareaSync(in)
	if dec.FlushOldFirst {
		// The buffer holds an unsaved edit of the OLD tile and is about to
		// be cleared. Post it first — same shape as the mode toggle's
		// save-before-clear (onToggleFileMode). The owning pane is the one
		// still text-descended into the old tile.
		if old := a.paneTextDescendedInto(a.lastTextareaTileID); old != nil {
			a.saveTextFromTextarea(old)
		} else {
			// No pane owns the old descent anymore (pane closed mid-edit).
			// The edit cannot be routed; surface instead of vanishing.
			js.Global().Get("console").Call("error",
				"gridwell: discarding unsaved text edit for tile "+a.lastTextareaTileID+" — owning pane is gone")
			a.reportErr(errsurface.Error, "textedit",
				"unsaved text edit discarded — its pane was closed before the save landed")
			a.textareaDirty = false
		}
	}
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
	r := paneRectFor(a, p)
	if r.W <= 0 || r.H <= 0 {
		return
	}
	left, top, width, height, fontPx := textTextareaBox(p, r)
	style := a.textTextarea.Get("style")
	setBoundsPx(style, left, top, width, height)
	style.Set("fontSize", pxf(fontPx))
	style.Set("clipPath", "none")
}

// textTextareaBox returns the textarea overlay's screen rectangle and
// font size for pane p with rect r. Adapter over panebox.TextareaBox
// supplying the wasm renderer's fixed-scale constants.
func textTextareaBox(_ *pane.Pane, r pane.Rect) (left, top, width, height, fontPx float64) {
	// Font size = the canvas painter's codePx so focused (textarea) and
	// blurred (canvas) raw text are the same size. See drawMarkdownText.
	b, fp := panebox.TextareaBox(r, textSideInset, defaultMarkdownStyle().codePx, textFixedScale)
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
	// No editable mode for source-backed text — toggle would put us in
	// a state with a visible textarea over a read-only blob.
	if g, ok := a.c.Grid(a.gridIDForPane(p)); ok {
		if file, ok := g.Tiles[p.TextFocus]; ok && a.tileReadOnly(&file) {
			return
		}
	}
	if p.TextMode == rpc.TextModeText {
		// Save before switching to rendered.
		a.saveTextFromTextarea(p)
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

// saveTextFromTextarea posts the textarea's value as the file's new
// content. The cached blob is updated under the file's *current*
// BlobID synchronously, so the immediate post-toggle render uses the
// user's typed content rather than the stale (pre-edit) blob. The
// RPC then runs async; on completion the cache is also updated under
// the new (content-hashed) BlobID, and the SSE NodeChanged event
// repoints the cached tile at it.
// paneTextDescendedInto returns the pane text-descended into the given tile,
// or nil. Used only by the rebind flush to route an unsaved buffer back to its
// owning descent — a mutation-time lookup, not a render-path read (the render
// path must never reach across panes; see drawMarkdownNode / issue #35).
func (a *App) paneTextDescendedInto(tileID string) *pane.Pane {
	if tileID == "" {
		return nil
	}
	var found *pane.Pane
	a.tree.Walk(func(p *pane.Pane) {
		if p.TextFocus == tileID && p.TextMode == rpc.TextModeText {
			found = p
		}
	})
	return found
}

func (a *App) saveTextFromTextarea(p *pane.Pane) {
	if a.textTextarea.IsUndefined() || a.textTextarea.IsNull() {
		return
	}
	buf := a.textTextarea.Get("value").String()
	gid := a.gridIDForPane(p)
	g, ok := a.c.Grid(gid)
	if !ok {
		// The save has nowhere to go — the edit would silently no-op while
		// the textarea keeps looking saved (charter §6).
		a.reportErr(errsurface.Error, "textedit", "text save failed — grid no longer loaded")
		return
	}
	file, ok := g.Tiles[p.TextFocus]
	if !ok {
		a.reportErr(errsurface.Error, "textedit", "text save failed — tile no longer exists")
		return
	}
	// Optimistic local render: write the typed content through the content
	// store (tile-scoped by tile id, so it never leaks into a clone), the same
	// accessor the renderer and parent-grid preview read.
	a.textareaDirty = false
	a.c.PutTileContent(file.ID, []byte(buf))
	go func() {
		a.postUpdateText(gid, &rpc.UpdateTextRequest{
			Path:    rpc.Path{WellIDs: p.Path},
			TileID:  file.ID,
			Version: file.Version,
			Data:    []byte(buf),
		}, []byte(buf))
	}()
}
