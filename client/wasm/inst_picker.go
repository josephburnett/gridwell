//go:build js && wasm

package main

import (
	"context"
	"fmt"
	"syscall/js"
	"time"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/instpick"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/schemaform"
)

// The instance picker (issue #251): the one dialog for a PARAMETERIZED
// plugin (rootless + instance grid — e.g. ssh's connections). It lists the
// plugin's existing instances by name — select one and you get THE SAME
// instance, never a copy (owner decision 2026-08-08) — with delete (the
// tombstone gesture, forever) and rename managed where the instance lives,
// plus the new-instance form from the plugin's creation schema below.
// Opened by the menu click on the plugin's swatch and by descending an
// unconfigured plugin well; the CALLER decides what selecting means
// (adopt into the well / descend) via onPick.
//
// Decisions live in client/instpick (entries, dedup, status, placement);
// this file is DOM and RPC plumbing. Data comes straight from the plugin:
// GetGrid on the instance grid (whose stamp carries the creation schema)
// plus one ReadContent per instance for its params document.

// pickReadyWait bounds how long the picker waits for a just-created
// instance's chain to be learned (the remote's first Info answer) before
// giving up and leaving the entry pending.
const pickReadyWait = 15 * time.Second

// openWellConfigurePicker is the descent gesture on an UNCONFIGURED plugin
// well: pick (or create) an instance, adopt its chain into the well, then
// complete the descent the user asked for. Dismissing leaves the well
// unconfigured — a legal, retryable state.
func (a *App) openWellConfigurePicker(p *pane.Pane, well *rpc.Tile, pl rpc.PluginInfo) {
	gid := a.gridIDForPane(p)
	paneID := p.ID
	wellID, wellVersion := well.ID, well.Version
	a.openInstancePicker(pl, func(e instpick.Entry) {
		req := &rpc.AdoptChildGridRequest{
			TileID: wellID, Version: wellVersion,
			ChildGridID: e.ChildGridID, Label: e.Name,
			ViewX: e.ViewX, ViewY: e.ViewY, ViewZoom: e.ViewZoom,
		}
		a.postTileMutate("AdoptChildGrid", gid, func(ctx context.Context) (*rpc.Tile, error) {
			return a.cl.AdoptChildGrid(ctx, req)
		}, func(t rpc.Tile) {
			if vp := a.tree.FindPane(paneID); vp != nil {
				a.startDescent(vp, &t)
			}
		})
	}, nil)
}

// openPluginVisitPicker is the menu-click / node-grid-click gesture on a
// PARAMETERIZED plugin: pick (or create) an instance and descend straight
// into it — a portal through a synthetic link well, exactly the shape a
// rooted plugin's click-enter uses, so ascent lands back here.
func (a *App) openPluginVisitPicker(p *pane.Pane, well *rpc.Tile, pl rpc.PluginInfo) {
	paneID := p.ID
	synthetic := *well
	a.openInstancePicker(pl, func(e instpick.Entry) {
		synthetic.ChildGridID = e.ChildGridID
		if e.Name != "" {
			synthetic.AltText = e.Name
		}
		synthetic.ViewX, synthetic.ViewY, synthetic.ViewZoom = e.ViewX, e.ViewY, e.ViewZoom
		synthetic.Reference = true // a portal crossing, like every plugin link
		if vp := a.tree.FindPane(paneID); vp != nil {
			a.flushFramingSave() // portal is a place change (issue #190)
			a.startDescent(vp, &synthetic)
		}
	}, nil)
}

// buildSchemaFieldRows appends one labeled input row per schema field to
// card and returns the inputs by field name — the one rendering of a
// creation schema (the picker's new-instance form is its only consumer
// since the configure-on-descent modal was retired with #251's flip).
func buildSchemaFieldRows(doc js.Value, card js.Value, form *schemaform.Form) map[string]js.Value {
	inputs := map[string]js.Value{}
	for _, fd := range form.Fields {
		row := doc.Call("createElement", "label")
		rs := row.Get("style")
		rs.Set("display", "block")
		rs.Set("margin", "6px 0")
		lbl := fd.Title
		if fd.Required {
			lbl += " *"
		}
		span := doc.Call("createElement", "span")
		span.Set("textContent", lbl)
		span.Get("style").Set("display", "inline-block")
		span.Get("style").Set("minWidth", "110px")
		row.Call("appendChild", span)
		var in js.Value
		if len(fd.Enum) > 0 {
			in = doc.Call("createElement", "select")
			empty := doc.Call("createElement", "option")
			empty.Set("value", "")
			empty.Set("textContent", "")
			in.Call("appendChild", empty)
			for _, e := range fd.Enum {
				opt := doc.Call("createElement", "option")
				opt.Set("value", e)
				opt.Set("textContent", e)
				in.Call("appendChild", opt)
			}
		} else {
			in = doc.Call("createElement", "input")
			switch {
			case fd.Secret:
				in.Set("type", "password") // masks INPUT only (schemaform doc)
			case fd.Type == "number":
				in.Set("type", "number")
			default:
				in.Set("type", "text")
			}
		}
		in.Set("name", fd.Name)
		is := in.Get("style")
		is.Set("background", "#1a1b20")
		is.Set("color", colorPlusFg)
		is.Set("border", "1px solid "+colorPaneBorder)
		is.Set("borderRadius", "4px")
		is.Set("padding", "3px 6px")
		is.Set("width", "170px")
		row.Call("appendChild", in)
		card.Call("appendChild", row)
		inputs[fd.Name] = in
	}
	return inputs
}

// instPickerEl lazily creates the shared modal container.
func (a *App) instPickerEl() js.Value {
	if a.instPicker.Truthy() {
		return a.instPicker
	}
	doc := a.doc
	modal := doc.Call("createElement", "div")
	modal.Set("id", "gw-inst-picker")
	st := modal.Get("style")
	st.Set("position", "fixed")
	st.Set("inset", "0")
	st.Set("display", "none")
	st.Set("alignItems", "center")
	st.Set("justifyContent", "center")
	st.Set("background", "rgba(0,0,0,0.45)")
	st.Set("zIndex", "20")
	doc.Get("body").Call("appendChild", modal)
	a.instPicker = modal
	return modal
}

// openInstancePicker opens the picker for plugin pl. onPick receives the
// chosen READY entry (existing or just created and connected); onCancel
// fires on dismiss. Exactly one of the two is called, once.
func (a *App) openInstancePicker(pl rpc.PluginInfo, onPick func(instpick.Entry), onCancel func()) {
	if a.instPickerOpen {
		return
	}
	a.instPickerOpen = true
	a.draw() // park live views (liveOverlaysHidden)

	doc := a.doc
	modal := a.instPickerEl()
	modal.Set("innerHTML", "")
	card := doc.Call("createElement", "div")
	cs := card.Get("style")
	cs.Set("background", colorPlusBg)
	cs.Set("border", "1px solid "+colorPaneBorder)
	cs.Set("borderRadius", "8px")
	cs.Set("padding", "16px 20px")
	cs.Set("minWidth", "380px")
	cs.Set("maxWidth", "520px")
	cs.Set("font", "13px sans-serif")
	cs.Set("color", colorPlusFg)
	titleEl := doc.Call("createElement", "div")
	titleEl.Set("textContent", pl.Label)
	titleEl.Get("style").Set("font", "bold 14px sans-serif")
	titleEl.Get("style").Set("marginBottom", "10px")
	card.Call("appendChild", titleEl)
	body := doc.Call("createElement", "div")
	body.Set("id", "gw-pick-body")
	body.Set("textContent", "loading…")
	card.Call("appendChild", body)
	modal.Call("appendChild", card)
	modal.Get("style").Set("display", "flex")
	a.centerCardOnActivePane(card)
	// Focus INSIDE the modal so Escape reaches its keydown listener even
	// before (or without) any field being focused — a click-opened picker
	// otherwise leaves focus on the canvas and can't be dismissed.
	card.Set("tabIndex", -1)
	card.Get("style").Set("outline", "none")
	card.Call("focus")

	done := false
	var keyCb, backdropCb js.Func
	close := func() {
		a.instPickerOpen = false
		defer a.draw() // un-park live views
		modal.Get("style").Set("display", "none")
		modal.Set("innerHTML", "")
		modal.Call("removeEventListener", "keydown", keyCb)
		modal.Call("removeEventListener", "mousedown", backdropCb)
		keyCb.Release()
		backdropCb.Release()
		a.releaseInstPickerFuncs()
		a.canvas.Call("focus")
	}
	finish := func(picked *instpick.Entry) {
		if done {
			return
		}
		done = true
		close()
		if picked != nil {
			onPick(*picked)
		} else if onCancel != nil {
			onCancel()
		}
	}
	keyCb = js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		ev := args[0]
		ev.Call("stopPropagation")
		if ev.Get("key").String() == "Escape" {
			ev.Call("preventDefault")
			finish(nil)
		}
		return nil
	})
	backdropCb = js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) > 0 && args[0].Get("target").Equal(modal) {
			finish(nil)
		}
		return nil
	})
	modal.Call("addEventListener", "keydown", keyCb)
	modal.Call("addEventListener", "mousedown", backdropCb)

	go a.loadInstancePicker(pl, card, body, finish)
}

// loadInstancePicker fetches the instance grid + per-instance params and
// renders the list and the new-instance form into body.
func (a *App) loadInstancePicker(pl rpc.PluginInfo, card, body js.Value, finish func(*instpick.Entry)) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := a.cl.GetGrid(ctx, pl.InstanceGridID)
	if err != nil {
		body.Set("textContent", "could not reach "+pl.Label+": "+err.Error())
		return
	}
	params := map[string]string{}
	for _, t := range resp.Tiles {
		if t.Kind != rpc.KindWell {
			continue
		}
		data, _, _, rerr := a.cl.ReadContent(ctx, t.ID)
		if rerr == nil && len(data) > 0 {
			params[t.ID] = string(data)
		}
	}
	entries := instpick.BuildEntries(resp.Tiles, func(id string) string { return params[id] })
	var form *schemaform.Form
	if raw, ok := resp.Grid.CreateSchemas[rpc.KindWell]; ok {
		form, err = schemaform.Parse(raw)
		if err != nil {
			a.reportErr(errsurface.Error, "plugin:"+pl.UUID, pl.Label+": unrenderable creation schema: "+err.Error())
		}
	}
	a.renderInstancePicker(pl, card, body, entries, form, finish)
}

// pickerFunc wraps a js callback and tracks it for release on the next
// re-render or close, so per-row listeners never leak across refreshes.
func (a *App) pickerFunc(fn func(this js.Value, args []js.Value) any) js.Func {
	f := js.FuncOf(fn)
	a.instPickerFuncs = append(a.instPickerFuncs, f)
	return f
}

// releaseInstPickerFuncs releases every callback the current render owns.
// The DOM nodes holding them are discarded wholesale (innerHTML reset), so
// no removeEventListener bookkeeping is needed — only the Go-side release.
func (a *App) releaseInstPickerFuncs() {
	for _, f := range a.instPickerFuncs {
		f.Release()
	}
	a.instPickerFuncs = nil
}

// renderInstancePicker draws the entry rows and the new-instance form.
func (a *App) renderInstancePicker(pl rpc.PluginInfo, card, body js.Value,
	entries []instpick.Entry, form *schemaform.Form, finish func(*instpick.Entry)) {
	doc := a.doc
	a.releaseInstPickerFuncs()
	body.Set("innerHTML", "")

	refresh := func() { go a.loadInstancePicker(pl, card, body, finish) }

	if len(entries) == 0 {
		empty := doc.Call("createElement", "div")
		empty.Set("textContent", "no "+pl.Label+" yet")
		empty.Get("style").Set("color", colorMuted)
		empty.Get("style").Set("margin", "4px 0 10px 0")
		body.Call("appendChild", empty)
	}
	for i, e := range entries {
		body.Call("appendChild", a.instanceRow(pl, i, e, form, refresh, finish))
	}

	if form != nil {
		body.Call("appendChild", a.newInstanceForm(pl, entries, form, finish))
	}
	a.centerCardOnActivePane(card)
}

// instanceRow builds one entry row: name + summary + status, click-to-pick
// when Ready, with rename and delete (two-click, it is FOREVER) affordances.
func (a *App) instanceRow(pl rpc.PluginInfo, idx int, e instpick.Entry,
	form *schemaform.Form, refresh func(), finish func(*instpick.Entry)) js.Value {
	doc := a.doc
	row := doc.Call("createElement", "div")
	row.Set("id", fmt.Sprintf("gw-pick-row-%d", idx))
	rs := row.Get("style")
	rs.Set("display", "flex")
	rs.Set("alignItems", "center")
	rs.Set("gap", "8px")
	rs.Set("padding", "5px 8px")
	rs.Set("margin", "2px 0")
	rs.Set("borderRadius", "5px")
	rs.Set("border", "1px solid "+colorPaneBorder)

	name := e.Name
	if name == "" {
		name = "unnamed"
	}
	nameEl := doc.Call("createElement", "span")
	nameEl.Set("textContent", name)
	nameEl.Get("style").Set("fontWeight", "bold")
	nameEl.Get("style").Set("whiteSpace", "nowrap")
	row.Call("appendChild", nameEl)

	sumEl := doc.Call("createElement", "span")
	sum := instpick.Summary(form, e.ParamsJSON)
	switch e.Status() {
	case instpick.Pending:
		sum = sum + e.PendingLabel()
	case instpick.Inert:
		sum = "unconfigured"
	}
	sumEl.Set("textContent", sum)
	ss := sumEl.Get("style")
	ss.Set("color", colorMuted)
	ss.Set("overflow", "hidden")
	ss.Set("textOverflow", "ellipsis")
	ss.Set("whiteSpace", "nowrap")
	ss.Set("flex", "1")
	row.Call("appendChild", sumEl)

	entry := e // captured per-row
	if e.Status() == instpick.Ready {
		row.Get("style").Set("cursor", "pointer")
		clickCb := a.pickerFunc(func(_ js.Value, _ []js.Value) any {
			// The buttons stop propagation; a row click is a pick.
			finish(&entry)
			return nil
		})
		row.Call("addEventListener", "click", clickCb)
	}

	row.Call("appendChild", a.instanceRenameButton(idx, entry, refresh))
	row.Call("appendChild", a.instanceDeleteButton(idx, entry, refresh))
	return row
}

// pickerButton builds a small inline action button that never triggers the
// row's pick click.
func (a *App) pickerButton(id, label string, onClick func(btn js.Value)) js.Value {
	doc := a.doc
	btn := doc.Call("createElement", "button")
	btn.Set("id", id)
	btn.Set("type", "button")
	btn.Set("textContent", label)
	bs := btn.Get("style")
	bs.Set("background", "#1a1b20")
	bs.Set("color", colorMuted)
	bs.Set("border", "1px solid "+colorPaneBorder)
	bs.Set("borderRadius", "4px")
	bs.Set("padding", "1px 7px")
	bs.Set("cursor", "pointer")
	bs.Set("font", "11px sans-serif")
	bs.Set("flexShrink", "0")
	cb := a.pickerFunc(func(_ js.Value, args []js.Value) any {
		if len(args) > 0 {
			args[0].Call("stopPropagation")
		}
		onClick(btn)
		return nil
	})
	btn.Call("addEventListener", "click", cb)
	return btn
}

// instanceRenameButton swaps the row into an inline name input; Enter
// commits the rename WHERE THE INSTANCE LIVES (the plugin's row — renaming
// your tile elsewhere never touches it, and vice versa).
func (a *App) instanceRenameButton(idx int, e instpick.Entry, refresh func()) js.Value {
	return a.pickerButton(fmt.Sprintf("gw-pick-ren-%d", idx), "rename", func(btn js.Value) {
		doc := a.doc
		in := doc.Call("createElement", "input")
		in.Set("type", "text")
		in.Set("value", e.Name)
		in.Set("id", fmt.Sprintf("gw-pick-ren-input-%d", idx))
		is := in.Get("style")
		is.Set("font", "12px sans-serif")
		is.Set("width", "120px")
		keyCb := a.pickerFunc(func(_ js.Value, args []js.Value) any {
			if len(args) == 0 {
				return nil
			}
			ev := args[0]
			ev.Call("stopPropagation")
			switch ev.Get("key").String() {
			case "Enter":
				// Through the one retained-rename commit (audit #10): a
				// transport failure parks the typed name for the retry kick
				// instead of discarding it with the input; the refresh runs
				// on success so the list shows the landed name.
				a.commitRenameRetained(e.TileID, in.Get("value").String(),
					func(*rpc.Tile) { refresh() })
			case "Escape":
				refresh()
			}
			return nil
		})
		in.Call("addEventListener", "keydown", keyCb)
		clickStop := a.pickerFunc(func(_ js.Value, args []js.Value) any {
			if len(args) > 0 {
				args[0].Call("stopPropagation")
			}
			return nil
		})
		in.Call("addEventListener", "click", clickStop)
		btn.Get("parentNode").Call("replaceChild", in, btn)
		in.Call("focus")
		in.Call("select")
	})
}

// instanceDeleteButton is the tombstone gesture: two clicks, and the second
// says what it means — the namespace segment dies FOREVER, and any tile
// still linked through this instance goes visibly broken.
func (a *App) instanceDeleteButton(idx int, e instpick.Entry, refresh func()) js.Value {
	armed := false
	return a.pickerButton(fmt.Sprintf("gw-pick-del-%d", idx), "delete", func(btn js.Value) {
		if !armed {
			armed = true
			btn.Set("textContent", "forever?")
			btn.Get("style").Set("color", colorCloseWarn)
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := a.cl.DeleteTile(ctx, &rpc.DeleteTileRequest{TileID: e.TileID, Version: e.Version}); err != nil {
				a.reportErr(errsurface.Error, "rpc:DeleteTile", "delete failed: "+rpcErrText(err))
			}
			refresh()
		}()
	})
}

// newInstanceForm renders the plugin's creation schema plus a name field.
// Submit dedups against the existing entries (identical details = the same
// instance — select it, never mint a twin); otherwise it creates the
// instance on the plugin's instance grid, commits the params, and waits for
// the chain before picking.
func (a *App) newInstanceForm(pl rpc.PluginInfo, entries []instpick.Entry,
	form *schemaform.Form, finish func(*instpick.Entry)) js.Value {
	doc := a.doc
	f := doc.Call("createElement", "form")
	f.Set("id", "gw-pick-new")
	fs := f.Get("style")
	fs.Set("marginTop", "12px")
	fs.Set("paddingTop", "10px")
	fs.Set("borderTop", "1px solid "+colorPaneBorder)

	head := doc.Call("createElement", "div")
	head.Set("textContent", "new")
	head.Get("style").Set("font", "bold 13px sans-serif")
	head.Get("style").Set("marginBottom", "6px")
	f.Call("appendChild", head)

	// The name is the instance's own fact (its row in the plugin), not a
	// schema param — kept out of the params document deliberately.
	nameRow := doc.Call("createElement", "label")
	nameRow.Get("style").Set("display", "block")
	nameRow.Get("style").Set("margin", "6px 0")
	nameSpan := doc.Call("createElement", "span")
	nameSpan.Set("textContent", "name")
	nameSpan.Get("style").Set("display", "inline-block")
	nameSpan.Get("style").Set("minWidth", "110px")
	nameRow.Call("appendChild", nameSpan)
	nameIn := doc.Call("createElement", "input")
	nameIn.Set("type", "text")
	nameIn.Set("id", "gw-pick-name")
	nis := nameIn.Get("style")
	nis.Set("background", "#1a1b20")
	nis.Set("color", colorPlusFg)
	nis.Set("border", "1px solid "+colorPaneBorder)
	nis.Set("borderRadius", "4px")
	nis.Set("padding", "3px 6px")
	nis.Set("width", "170px")
	nameRow.Call("appendChild", nameIn)
	f.Call("appendChild", nameRow)

	inputs := buildSchemaFieldRows(doc, f, form)

	errEl := doc.Call("createElement", "div")
	errEl.Set("id", "gw-pick-err")
	errEl.Get("style").Set("color", colorCloseWarn)
	errEl.Get("style").Set("minHeight", "16px")
	errEl.Get("style").Set("margin", "6px 0")
	f.Call("appendChild", errEl)

	btnRow := doc.Call("createElement", "div")
	btnRow.Get("style").Set("textAlign", "right")
	okBtn := doc.Call("createElement", "button")
	okBtn.Set("type", "submit")
	okBtn.Set("id", "gw-pick-create")
	okBtn.Set("textContent", "create")
	btnRow.Call("appendChild", okBtn)
	f.Call("appendChild", btnRow)

	submitCb := a.pickerFunc(func(_ js.Value, args []js.Value) any {
		if len(args) > 0 {
			args[0].Call("preventDefault")
		}
		values := map[string]string{}
		for name, in := range inputs {
			values[name] = in.Get("value").String()
		}
		if errs := form.Validate(values); len(errs) > 0 {
			errEl.Set("textContent", errs[0])
			return nil
		}
		params, err := form.Encode(values)
		if err != nil {
			errEl.Set("textContent", err.Error())
			return nil
		}
		if m := instpick.Match(entries, params); m != nil {
			// Identical details = the same instance (owner decision).
			if m.Status() == instpick.Ready {
				finish(m)
			} else {
				errEl.Set("textContent", "these details already exist ("+displayName(m.Name)+") — still connecting")
			}
			return nil
		}
		errEl.Set("textContent", "creating…")
		go a.createInstance(pl, nameIn.Get("value").String(), params, errEl, finish)
		return nil
	})
	f.Call("addEventListener", "submit", submitCb)
	return f
}

func displayName(n string) string {
	if n == "" {
		return "unnamed"
	}
	return n
}

// createInstance creates the instance well on the plugin's instance grid,
// commits its params (the plugin validates authoritatively and mints the
// namespace segment), then waits for the chain to be learned. Success picks
// the new entry; an unreachable remote leaves the instance PENDING — it
// stays in the list for a later attempt, and the caller's well stays
// unconfigured.
func (a *App) createInstance(pl rpc.PluginInfo, name string, params []byte,
	errEl js.Value, finish func(*instpick.Entry)) {
	ctx, cancel := context.WithTimeout(context.Background(), pickReadyWait+15*time.Second)
	defer cancel()
	resp, err := a.cl.GetGrid(ctx, pl.InstanceGridID)
	if err != nil {
		errEl.Set("textContent", err.Error())
		return
	}
	x, y := instpick.FreeCell(resp.Tiles)
	tile, err := a.cl.CreateWell(ctx, &rpc.CreateWellRequest{
		GridID: pl.InstanceGridID, X: x, Y: y, W: 1, H: 1,
	})
	if err != nil {
		errEl.Set("textContent", err.Error())
		return
	}
	if name != "" {
		// A typed name is a USER name: it must ride the rename gesture so
		// the alt_user latch holds it over the plugin's automatic label
		// (ssh's "user@host" capture at params commit).
		tile, err = a.cl.RenameTile(ctx, tile.ID, tile.Version, name)
		if err != nil {
			errEl.Set("textContent", err.Error())
			return
		}
	}
	tile, err = a.cl.WriteContent(ctx, tile.ID, tile.Version, params)
	if err != nil {
		// The params were refused (the plugin is the authority). The empty
		// instance row stays — visible and deletable, never silent.
		errEl.Set("textContent", err.Error())
		return
	}
	errEl.Set("textContent", "connecting…")
	deadline := time.Now().Add(pickReadyWait)
	detail := ""
	for time.Now().Before(deadline) {
		cur, gerr := a.cl.GetTile(ctx, tile.ID)
		if gerr == nil && cur.ChildGridID != "" {
			finish(&instpick.Entry{
				TileID: cur.ID, Version: cur.Version, Name: cur.AltText,
				ParamsJSON: string(params), ChildGridID: cur.ChildGridID,
				ViewX: cur.ViewX, ViewY: cur.ViewY, ViewZoom: cur.ViewZoom,
			})
			return
		}
		// The plugin records WHY the connection isn't up (Tile.StatusDetail);
		// show it the moment it exists — the wait must never hide the reason.
		if gerr == nil && cur.StatusDetail != "" && cur.StatusDetail != detail {
			detail = cur.StatusDetail
			errEl.Set("textContent", "connecting… — "+detail)
		}
		time.Sleep(500 * time.Millisecond)
	}
	// Created but not reachable: an honest, durable state — the entry is in
	// the list as (connecting…) and a later open can pick it.
	msg := "created, but the remote hasn't answered — it stays listed as connecting"
	if detail != "" {
		msg = "created, but the remote hasn't answered: " + detail + " — it stays listed as connecting"
	}
	errEl.Set("textContent", msg)
}
