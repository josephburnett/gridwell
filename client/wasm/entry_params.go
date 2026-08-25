//go:build js && wasm

package main

// Plugin menu-entry tiles (#258), the client half: creating one from the
// palette and prompting for its parameters on first descent (#209's
// drop-first rule — the drop never prompts). The params commit as the
// tile's CONTENT through WriteContent: the plugin validates
// authoritatively and reacts (fs's search runs the query and fills the
// child grid). The form machinery is the shared modal shell +
// schemaform field rows (modal_form.go).

import (
	"context"
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/client/schemaform"
)

// menuEntryFor resolves the MenuEntry declaration a tile was minted from:
// the tile's grid's stamped entries, by id. ok=false when the grid isn't
// cached or the plugin no longer declares the entry (the tile still
// renders as its plain kind — a tool whose plugin forgot it degrades to
// ordinary content, never an error).
func (a *App) menuEntryFor(t *rpc.Tile) (rpc.MenuEntry, bool) {
	g, ok := a.c.Grid(t.GridID)
	if !ok {
		return rpc.MenuEntry{}, false
	}
	for _, e := range g.Meta.MenuEntries {
		if e.ID == t.MenuEntry {
			return e, true
		}
	}
	return rpc.MenuEntry{}, false
}

// entryTileNeedsParams reports that descending t should prompt for the
// entry's parameters first: an entry-minted tile whose params were never
// committed (no content yet) and whose entry declares a schema.
func (a *App) entryTileNeedsParams(t *rpc.Tile) bool {
	if t.MenuEntry == "" {
		return false
	}
	// Params live in the tile's content: a well-kind tool gains its child
	// on commit; leaf kinds gain a blob. Either fact present = configured.
	if t.ChildGridID != "" || t.BlobID != 0 {
		return false
	}
	e, ok := a.menuEntryFor(t)
	return ok && e.ParamSchema != ""
}

// openEntryParamsForm prompts for an entry tile's parameters (the shared
// modal + schemaform rows) and commits them as the tile's content. On
// success the grid refetches — a well-kind tool's child appears — and,
// when the tile then has a child, the descent completes into it.
func (a *App) openEntryParamsForm(p *pane.Pane, t *rpc.Tile) {
	e, ok := a.menuEntryFor(t)
	if !ok || e.ParamSchema == "" {
		return
	}
	form, err := schemaform.Parse(e.ParamSchema)
	if err != nil {
		a.reportErr(errsurface.Error, "entry", "entry form: "+err.Error())
		return
	}
	if a.modalOpen {
		return
	}
	a.modalOpen = true
	a.draw()

	doc := a.doc
	modal := a.modalCardEl()
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
	label := e.Label
	if label == "" {
		label = e.ID
	}
	titleEl.Set("textContent", label)
	titleEl.Get("style").Set("font", "bold 14px sans-serif")
	titleEl.Get("style").Set("marginBottom", "10px")
	card.Call("appendChild", titleEl)

	f := doc.Call("createElement", "form")
	f.Set("id", "gw-entry-form")
	inputs := buildSchemaFieldRows(doc, f, form)
	errEl := doc.Call("createElement", "div")
	errEl.Set("id", "gw-entry-err")
	errEl.Get("style").Set("color", colorCloseWarn)
	errEl.Get("style").Set("minHeight", "16px")
	errEl.Get("style").Set("margin", "6px 0")
	f.Call("appendChild", errEl)
	btnRow := doc.Call("createElement", "div")
	btnRow.Get("style").Set("textAlign", "right")
	okBtn := doc.Call("createElement", "button")
	okBtn.Set("type", "submit")
	okBtn.Set("id", "gw-entry-ok")
	okBtn.Set("textContent", "go")
	btnRow.Call("appendChild", okBtn)
	f.Call("appendChild", btnRow)
	card.Call("appendChild", f)
	modal.Call("appendChild", card)
	modal.Get("style").Set("display", "flex")
	a.centerCardOnActivePane(card)
	card.Set("tabIndex", -1)
	card.Call("focus")

	paneID, tileID, version, gid := p.ID, t.ID, t.Version, t.GridID
	close := func() {
		modal.Get("style").Set("display", "none")
		a.modalOpen = false
		a.releaseModalFuncs()
		a.draw()
	}
	submitCb := a.modalFunc(func(_ js.Value, args []js.Value) any {
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
		go func() {
			// The params ARE the tile's content (the connection-well
			// pattern): versioned, validated by the plugin at commit.
			nt, err := a.cl.WriteContent(context.Background(), tileID, version, params)
			if err != nil {
				a.reportErr(errsurface.Error, "entry", label+": "+rpcErrText(err))
				return
			}
			a.c.UpdateTile(nt.GridID, *nt)
			close()
			a.fetchGrid(gid)
			// A well-kind tool gains its child at commit — complete the
			// user's descent into it.
			if nt.ChildGridID != "" {
				if fp := a.tree.FindPane(paneID); fp != nil {
					a.startDescent(fp, nt)
				}
			}
			a.draw()
		}()
		return nil
	})
	f.Call("addEventListener", "submit", submitCb)
	keyCb := a.modalFunc(func(_ js.Value, args []js.Value) any {
		if len(args) > 0 && args[0].Get("key").String() == "Escape" {
			args[0].Call("preventDefault")
			close()
		}
		return nil
	})
	card.Call("addEventListener", "keydown", keyCb)
}
