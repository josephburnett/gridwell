//go:build js && wasm

package main

import (
	"strconv"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/gesture"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/panebox"
	"github.com/josephburnett/gridwell/client/textedit"
)

// textSaveDebounceMs is the delay from the first keystroke since the last save
// to the next save fire, so continuous typing saves at most once per interval.
const textSaveDebounceMs = 600

// pxf is the one place the overlay code turns a float coordinate into a CSS
// style value.
func pxf(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) + "px" }

func setBoundsPx(style js.Value, left, top, width, height float64) {
	style.Set("left", pxf(left))
	style.Set("top", pxf(top))
	style.Set("width", pxf(width))
	style.Set("height", pxf(height))
}

// scheduleFileSave no-ops when a save is already pending, so every keystroke
// can call it.
func (a *App) scheduleFileSave() {
	a.persist.sched.textSave.arm(textSaveDebounceMs)
}

// textFitZoom returns the parent zoom at which the text tile's cell footprint
// exactly fits pane rect r's inner box. It fits by the min of the dimension
// ratios, not the max, because the binding dimension is what limits live
// content, so preview text fills the tile at the same fraction.
func textFitZoom(r pane.Rect, fileW, fileH int64) float64 {
	return panebox.FitZoom(r, fileW, fileH, textSideInset, cellPx)
}

// textInnerBox returns the screen rectangle of a text-focused pane's inner
// reading area. The painter, the markdown renderer, the textarea positioner
// and the click hit-test all use it, so "inside" means the same to all of
// them. URL tiles use paneContentBox instead.
func textInnerBox(r pane.Rect) (x, y, w, h float64) {
	b := panebox.InnerBox(r, textSideInset)
	return b.X, b.Y, b.W, b.H
}

// paneContentBox returns the rectangle a live surface, and its parked fallback
// frame, occupies in pane rect r: the pane minus the outline. The one bar sits
// below every pane, so web content fills this edge to edge.
func paneContentBox(r pane.Rect) (x, y, w, h float64) {
	b := panebox.ContentBox(r, paneBorderPx)
	return b.X, b.Y, b.W, b.H
}

func pointInPaneContent(r pane.Rect, sx, sy float64) bool {
	return panebox.PointInContent(r, paneBorderPx, sx, sy)
}

func pointInFileInner(r pane.Rect, sx, sy float64) bool {
	return panebox.PointInInner(r, textSideInset, sx, sy)
}

// liveViewOwnsPoint is the one decision behind handing a pointer event to the
// native view instead of acting on it. It supplies the two facts the shim
// owns, so no handler re-derives either.
func (a *App) liveViewOwnsPoint(p *pane.Pane, r pane.Rect, sx, sy float64) bool {
	if p == nil {
		return false
	}
	return panebox.LiveViewOwnsPoint(a.liveOverlaysHidden(), a.urlViewFor(p.ID) != nil, r, paneBorderPx, sx, sy)
}

// hasTextarea reports whether the singleton text-overlay element exists yet: a
// draw, a URL read, or a mode toggle can arrive before ensureFileTextarea.
func (a *App) hasTextarea() bool {
	return !a.overlays.textTextarea.IsUndefined() && !a.overlays.textTextarea.IsNull()
}

// ensureFileTextarea creates, once, the shared <textarea> overlay for markdown
// text-mode editing. It lives in document.body and is shown only by
// refreshFileOverlay.
func (a *App) ensureFileTextarea() {
	if a.hasTextarea() {
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
	// border-box so the padding fits inside the width and height
	// textTextareaBox returns; content-box would overhang the pane's right
	// and bottom border strokes.
	style.Set("boxSizing", "border-box")
	// Metrics mirror drawMarkdownText exactly so raw text does not reflow when
	// focus enters or leaves the pane. The painter replicates this line box's
	// baseline, not the other way round, so the inset here stays uniform.
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

	a.persist.sched.textSave.set(func() {
		// Sweep every dirty content entry, whoever holds focus now. A sweep
		// over tile-keyed entries cannot strand an edit whose pane moved on,
		// so no fire-time guard on focus or mode is needed.
		a.flushDirtyText()
	})
	a.overlays.textTextareaInputCb = js.FuncOf(func(this js.Value, args []js.Value) any {
		// Mirror the keystroke into the cache under the tile the textarea is
		// bound to, the one owner of unsaved text. The DOM value is a view;
		// nothing persists from it directly.
		a.overlays.textareaReady = true
		if a.overlays.lastTextareaTileID == "" {
			// An unbound textarea is hidden, so this should be unreachable.
			// Surface it rather than let the typing vanish silently.
			a.reportErr(errsurface.Error, "textedit",
				"typing arrived with no bound tile — this edit cannot be saved")
			return nil
		}
		// Keyed by contentKey: a leaf link's edits accumulate under its
		// target's id, the one shared content fact; see text_flush.go.
		a.putEditedContent(a.contentKey(a.overlays.lastTextareaTileID), []byte(a.overlays.textTextarea.Get("value").String()))
		a.scheduleFileSave()
		a.draw()
		a.scheduleURLUpdate()
		return nil
	})
	ta.Call("addEventListener", "input", a.overlays.textTextareaInputCb)

	// Cursor moves without text changes also refresh the URL; input handles
	// typed changes.
	cursorCb := js.FuncOf(func(this js.Value, args []js.Value) any {
		a.scheduleURLUpdate()
		return nil
	})
	ta.Call("addEventListener", "keyup", cursorCb)
	ta.Call("addEventListener", "mouseup", cursorCb)
	ta.Call("addEventListener", "select", cursorCb)

	a.overlays.textTextareaScrollCb = js.FuncOf(func(this js.Value, args []js.Value) any {
		// Mirror the browser scroll onto the focused pane so SetTextView on
		// ascent persists the right value, but only while the textarea is
		// bound to that pane's tile: a stale binding would land tile A's
		// scroll offset on tile B's text_y.
		p := a.tree.FocusedPane()
		if p == nil || p.ContentID() == "" || p.ContentID() != a.overlays.lastTextareaTileID {
			return nil
		}
		p.TextScrollY = a.overlays.textTextarea.Get("scrollTop").Float()
		return nil
	})
	ta.Call("addEventListener", "scroll", a.overlays.textTextareaScrollCb)

	// No wheel listener: text mode uses the textarea's native scroll, and
	// TextZoom is fixed for the visit.

	// The textarea covers the whole pane in text mode, so canvas click handlers
	// never see clicks here. An edge-zone left mousedown ascends; a right
	// mousedown goes through the canvas listener's own entry point, so split,
	// swap and resize work over the textarea.
	mdCb := js.FuncOf(func(this js.Value, args []js.Value) any {
		ev := args[0]
		button := ev.Get("button").Int()
		canvasRect := a.canvas.Call("getBoundingClientRect")
		sx := ev.Get("clientX").Float() - canvasRect.Get("left").Float()
		sy := ev.Get("clientY").Float() - canvasRect.Get("top").Float()
		if button == 2 {
			ev.Call("preventDefault")
			if a.trans.Any() {
				return nil
			}
			p, r, ok := a.paneAtScreen(sx, sy)
			if !ok {
				return nil
			}
			a.onRightDown(p, r, sx, sy, rightDragIntent(ev))
			return nil
		}
		if button == 1 {
			// Middle-click ascends, same as on the canvas, which never sees
			// this press.
			ev.Call("preventDefault")
			if a.trans.Any() {
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
		if p == nil || p.ContentID() == "" {
			return nil
		}
		r := paneRectFor(a, p)
		if !pointInFileInner(r, sx, sy) {
			ev.Call("preventDefault")
			a.ascend(p, 1, true)
		}
		return nil
	})
	ta.Call("addEventListener", "mousedown", mdCb)

	// Forward to the right-button handlers while a right-drag is in flight;
	// without this, dragging over the textarea would freeze the gesture.
	mmCb := js.FuncOf(func(this js.Value, args []js.Value) any {
		if a.rightDrag == nil {
			return nil
		}
		ev := args[0]
		canvasRect := a.canvas.Call("getBoundingClientRect")
		sx := ev.Get("clientX").Float() - canvasRect.Get("left").Float()
		sy := ev.Get("clientY").Float() - canvasRect.Get("top").Float()
		// The release may have happened somewhere we did not see. Only the
		// right drag is forwarded here, so that is the only arm offered.
		buttons := ev.Get("buttons").Int()
		if gesture.RecoverRelease(buttons, gesture.Armed{RightDrag: true}) == gesture.FinishRightDrag {
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
	cmCb := js.FuncOf(func(this js.Value, args []js.Value) any {
		args[0].Call("preventDefault")
		return nil
	})
	ta.Call("addEventListener", "contextmenu", cmCb)

	// Multi-finger touches forward into the touch gesture machine, the analogue
	// of the mouse forwarding above. Single-finger touches keep native textarea
	// behavior: caret, selection, the OS keyboard.
	a.installTextareaTouch(ta)

	a.doc.Get("body").Call("appendChild", ta)
	a.overlays.textTextarea = ta
}

// ensureFileToggle creates, once, the floating rendered/raw toggle button. Its
// zIndex 6 layers it above the textarea's 5, so the text can fill the pane.
func (a *App) ensureFileToggle() {
	if !a.overlays.textToggleBtn.IsUndefined() && !a.overlays.textToggleBtn.IsNull() {
		return
	}
	btn := a.doc.Call("createElement", "div")
	btn.Set("id", "gw-text-toggle")
	style := btn.Get("style")
	style.Set("position", "absolute")
	style.Set("display", "none")
	style.Set("boxSizing", "border-box")
	style.Set("width", pxf(2*plusButtonRadius))
	style.Set("height", pxf(2*plusButtonRadius))
	style.Set("borderRadius", "50%")
	// background and color come from barTheme on every refreshFileToggle, so
	// there is no second, frozen copy of the theme fact.
	style.Set("border", "1px solid #dff4f4")
	style.Set("cursor", "pointer")
	style.Set("alignItems", "center")
	style.Set("justifyContent", "center")
	style.Set("zIndex", "6")
	style.Set("userSelect", "none")
	style.Set("fontSize", "18px")
	btn.Set("textContent", "a")

	a.overlays.textToggleCb = js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		ev := args[0]
		ev.Call("preventDefault")
		ev.Call("stopPropagation")
		p := a.tree.FocusedPane()
		if p == nil || p.ContentID() == "" {
			return nil
		}
		// A right-click does nothing: ascent is the previous crumb.
		if ev.Get("button").Int() != 0 {
			return nil
		}
		a.onToggleFileMode(p)
		return nil
	})
	btn.Call("addEventListener", "mousedown", a.overlays.textToggleCb)
	// A right-click on the toggle stays inert instead of popping a menu.
	btn.Call("addEventListener", "contextmenu", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) > 0 {
			args[0].Call("preventDefault")
		}
		return nil
	}))
	// The shared translation routes a tap here as a left mousedown.
	a.installOverlayTouch(btn, nil)
	a.doc.Get("body").Call("appendChild", btn)
	a.overlays.textToggleBtn = btn
}

// refreshFileToggle shows the toggle for a markdown descent in either mode and
// hides it otherwise. A url tile uses a canvas back button instead, and a page
// tile has no document body to show a second face of.
func (a *App) refreshFileToggle() {
	a.ensureFileToggle()
	style := a.overlays.textToggleBtn.Get("style")
	hide := func() { style.Set("display", "none") }

	p := a.tree.FocusedPane()
	if p == nil || p.ContentID() == "" || a.isURLDescent(p) {
		hide()
		return
	}
	gid := a.gridIDForPane(p)
	g, ok := a.c.Grid(gid)
	if !ok {
		hide()
		return
	}
	file, ok := g.Tiles[p.ContentID()]
	if !ok || !rpc.TextDocument(file) {
		hide()
		return
	}
	if !textedit.ToggleVisible(file, a.tileReadOnly(file)) {
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
	// The same family shades as the canvas slot buttons.
	band, button := a.barTheme()
	style.Set("background", button)
	style.Set("color", band)
	// The glyph names the target mode: an italic serif "a" renders, a
	// monospace "a" edits the source.
	if p.TextMode == rpc.TextModeRendered {
		style.Set("fontFamily", `ui-monospace, "SF Mono", Menlo, Consolas, monospace`)
		style.Set("fontStyle", "normal")
	} else {
		style.Set("fontFamily", `ui-serif, "Times New Roman", Georgia, serif`)
		style.Set("fontStyle", "italic")
	}
	style.Set("display", "flex")
}

// refreshFileOverlay shows or hides the textarea for the focused pane,
// whenever pane state changes: a descent, a mode toggle, an ascent.
func (a *App) refreshFileOverlay() {
	a.refreshFileToggle()
	a.refreshRenderedOverlay()
	a.ensureFileTextarea()
	ta := a.overlays.textTextarea

	p := a.tree.FocusedPane()
	if p == nil || p.ContentID() == "" || p.TextMode != rpc.TextModeText {
		ta.Get("style").Set("display", "none")
		// Back to the canvas so ascent and other gestures keep working.
		a.focusCanvas()
		return
	}
	// Source-backed text tiles are read-only. The server-stored mode can
	// outlive the source key being set, so a stale "text" must not show the
	// textarea.
	if g, ok := a.c.Grid(a.gridIDForPane(p)); ok {
		if file, ok := g.Tiles[p.ContentID()]; ok && a.tileReadOnly(file) {
			ta.Get("style").Set("display", "none")
			a.focusCanvas()
			return
		}
	}
	r := paneRectFor(a, p)
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

	// Sync the textarea singleton to the focused tile; textedit.DecideTextareaSync
	// owns the decision. On a tile switch it clears immediately, before the
	// blob loads, so the previous tile's buffer never appears as the new
	// tile's default; the fetch's onComplete calls back here with it.
	gid := a.gridIDForPane(p)
	_, pendingEdit := a.c.DirtyContent(a.contentKey(a.overlays.lastTextareaTileID))
	in := textedit.TextareaSyncInput{
		FocusedTileID: p.ContentID(),
		LastTileID:    a.overlays.lastTextareaTileID,
		CurrentValue:  ta.Get("value").String(),
		PendingEdit:   pendingEdit,
	}
	if g, ok := a.c.Grid(gid); ok {
		if file, ok := g.Tiles[p.ContentID()]; ok {
			if body, ok := a.tileBody(file); ok {
				in.BlobCached = true
				in.BlobContent = string(body)
			}
		}
	}
	// A rebind rescues nothing and discards nothing: the old tile's typing
	// lives in its own cache entry, and the dirty sweep posts it.
	dec := textedit.DecideTextareaSync(in)
	if dec.SetValue {
		ta.Set("value", dec.Value)
		// For textedit.CanvasHiddenByOverlay: false means the textarea was
		// cleared on a tile switch, or the blob has not arrived, and the
		// canvas keeps painting through the loading race.
		a.overlays.textareaReady = dec.Value != ""
	}
	a.overlays.lastTextareaTileID = dec.NewLastTileID
	// Reflect saved scroll in; on later calls the user's own scroll wins.
	if ta.Get("scrollTop").Float() == 0 && p.TextScrollY > 0 {
		ta.Set("scrollTop", p.TextScrollY)
	}
	ta.Call("focus")
}

// focusCanvas returns keyboard focus to the canvas unless the inline rename
// input is open. It runs on every async overlay refresh, and would otherwise
// yank focus out of a just-opened rename input, which blur would then commit.
func (a *App) focusCanvas() {
	if a.overlays.renameEditing {
		return
	}
	a.canvas.Call("focus")
}

// syncTextOverlayPosition repositions an already-shown textarea every draw so
// it tracks the focused pane. It does not refocus or mutate the value.
func (a *App) syncTextOverlayPosition() {
	a.refreshFileToggle()
	if !a.hasTextarea() {
		return
	}
	display := a.overlays.textTextarea.Get("style").Get("display").String()
	if display == "none" {
		return
	}
	p := a.tree.FocusedPane()
	if p == nil || p.ContentID() == "" || p.TextMode != rpc.TextModeText {
		a.overlays.textTextarea.Get("style").Set("display", "none")
		return
	}
	r := paneRectFor(a, p)
	if r.W <= 0 || r.H <= 0 {
		return
	}
	left, top, width, height, fontPx := a.textTextareaBox(p, r)
	style := a.overlays.textTextarea.Get("style")
	setBoundsPx(style, left, top, width, height)
	style.Set("fontSize", pxf(fontPx))
	style.Set("clipPath", "none")
}

func (a *App) textTextareaBox(p *pane.Pane, r pane.Rect) (left, top, width, height, fontPx float64) {
	// The font size is the canvas painter's codePx at the pane's live scale,
	// so focused and blurred raw text are the same size. See drawMarkdownText.
	b, fp := panebox.TextareaBox(r, textSideInset, defaultMarkdownStyle().codePx, a.textScaleFor(p))
	return b.X, b.Y, b.W, b.H, fp
}

// textSideInset is the reading margin between the pane edge and the text. It
// is fixed, independent of paneBorderPx, so thinning the colored border does
// not cram text against the edge.
const textSideInset = 6.0

// onToggleFileMode saves the current buffer before switching to rendered.
func (a *App) onToggleFileMode(p *pane.Pane) {
	if p.ContentID() == "" {
		return
	}
	// A read-only non-renderable tile has no mode to flip to.
	if g, ok := a.c.Grid(a.gridIDForPane(p)); ok {
		if file, ok := g.Tiles[p.ContentID()]; ok && !textedit.ToggleVisible(file, a.tileReadOnly(file)) {
			return
		}
	}
	if p.TextMode == rpc.TextModeText {
		// Flush pending typing from the cache, where the keystrokes live,
		// never from the DOM.
		a.flushTileContent(p.ContentID())
		p.TextMode = rpc.TextModeRendered
	} else {
		p.TextMode = rpc.TextModeText
		// Cleared so refreshFileOverlay picks up the freshest cached blob.
		if a.hasTextarea() {
			a.overlays.textTextarea.Set("value", "")
			a.overlays.textareaReady = false
		}
	}
	// The mode persists to the tile on ascent, in saveTextBeforeAscent; while
	// descended the focused pane's live TextMode drives the preview.
	a.refreshFileOverlay()
	a.draw()
	a.scheduleURLUpdate()
}
