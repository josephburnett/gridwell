//go:build js && wasm

package main

import (
	"syscall/js"

	"github.com/josephburnett/gridwell/client/errsurface"
	"github.com/josephburnett/gridwell/client/schemaform"
)

// The creation-parameter modal (issue #198): when the destination grid's
// plugin declares a create schema for a tile kind (Grid.CreateSchemas — the
// per-grid stamp, verbatim through mounts), the create gesture opens this
// form instead of committing immediately. Submit encodes the values as the
// params document (the created tile's CONTENT, committed via WriteContent);
// the plugin validates authoritatively at commit — this form's validation
// is UX only. Built dynamically (fields vary per schema) inside a lazily
// created container, mirroring the URL modal's open/close discipline:
// listeners installed per open, released on close; live views parked while
// open (liveOverlaysHidden consults schemaModalOpen).

// schemaModalEl lazily creates the shared modal container.
func (a *App) schemaModalEl() js.Value {
	if a.schemaModal.Truthy() {
		return a.schemaModal
	}
	doc := a.doc
	modal := doc.Call("createElement", "div")
	modal.Set("id", "gw-schema-modal")
	st := modal.Get("style")
	st.Set("position", "fixed")
	st.Set("inset", "0")
	st.Set("display", "none")
	st.Set("alignItems", "center")
	st.Set("justifyContent", "center")
	st.Set("background", "rgba(0,0,0,0.45)")
	st.Set("zIndex", "20")
	doc.Get("body").Call("appendChild", modal)
	a.schemaModal = modal
	return modal
}

// openSchemaModal renders the parsed form and resolves to onSubmit with the
// encoded params document, or onCancel on dismiss (Escape / cancel button /
// backdrop click).
func (a *App) openSchemaModal(title string, form *schemaform.Form, onSubmit func(params []byte), onCancel func()) {
	if a.schemaModalOpen {
		return
	}
	a.schemaModalOpen = true
	a.draw() // park live views (liveOverlaysHidden)

	doc := a.doc
	modal := a.schemaModalEl()
	modal.Set("innerHTML", "")
	card := doc.Call("createElement", "form")
	cs := card.Get("style")
	cs.Set("background", colorPlusBg)
	cs.Set("border", "1px solid "+colorPaneBorder)
	cs.Set("borderRadius", "8px")
	cs.Set("padding", "16px 20px")
	cs.Set("minWidth", "320px")
	cs.Set("font", "13px sans-serif")
	cs.Set("color", colorPlusFg)
	titleEl := doc.Call("createElement", "div")
	titleEl.Set("textContent", title)
	titleEl.Get("style").Set("marginBottom", "10px")
	titleEl.Get("style").Set("font", "bold 14px sans-serif")
	card.Call("appendChild", titleEl)

	inputs := buildSchemaFieldRows(doc, card, form)

	errEl := doc.Call("createElement", "div")
	errEl.Get("style").Set("color", colorCloseWarn)
	errEl.Get("style").Set("minHeight", "16px")
	errEl.Get("style").Set("margin", "6px 0")
	card.Call("appendChild", errEl)

	btnRow := doc.Call("createElement", "div")
	btnRow.Get("style").Set("textAlign", "right")
	cancelBtn := doc.Call("createElement", "button")
	cancelBtn.Set("type", "button")
	cancelBtn.Set("textContent", "cancel")
	okBtn := doc.Call("createElement", "button")
	okBtn.Set("type", "submit")
	okBtn.Set("textContent", "create")
	okBtn.Get("style").Set("marginLeft", "8px")
	btnRow.Call("appendChild", cancelBtn)
	btnRow.Call("appendChild", okBtn)
	card.Call("appendChild", btnRow)
	modal.Call("appendChild", card)
	modal.Get("style").Set("display", "flex")
	a.centerCardOnActivePane(card)

	var submitCb, cancelCb, keyCb, backdropCb js.Func
	close := func() {
		a.schemaModalOpen = false
		defer a.draw() // un-park live views
		modal.Get("style").Set("display", "none")
		modal.Set("innerHTML", "")
		card.Call("removeEventListener", "submit", submitCb)
		cancelBtn.Call("removeEventListener", "click", cancelCb)
		modal.Call("removeEventListener", "keydown", keyCb)
		modal.Call("removeEventListener", "mousedown", backdropCb)
		submitCb.Release()
		cancelCb.Release()
		keyCb.Release()
		backdropCb.Release()
		a.canvas.Call("focus")
	}
	cancel := func() {
		close()
		if onCancel != nil {
			onCancel()
		}
	}
	submitCb = js.FuncOf(func(_ js.Value, args []js.Value) any {
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
		close()
		onSubmit(params)
		return nil
	})
	cancelCb = js.FuncOf(func(_ js.Value, _ []js.Value) any { cancel(); return nil })
	keyCb = js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		ev := args[0]
		ev.Call("stopPropagation") // keep keys out of the canvas handlers
		if ev.Get("key").String() == "Escape" {
			ev.Call("preventDefault")
			cancel()
		}
		return nil
	})
	backdropCb = js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) > 0 && args[0].Get("target").Equal(modal) {
			cancel()
		}
		return nil
	})
	card.Call("addEventListener", "submit", submitCb)
	cancelBtn.Call("addEventListener", "click", cancelCb)
	modal.Call("addEventListener", "keydown", keyCb)
	modal.Call("addEventListener", "mousedown", backdropCb)

	// Focus the first field.
	for _, fd := range form.Fields {
		inputs[fd.Name].Call("focus")
		break
	}
}

// buildSchemaFieldRows appends one labeled input row per schema field to
// card and returns the inputs by field name. Shared by the schema modal and
// the instance picker's new-instance form — one rendering of a creation
// schema, not two.
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

// createSchemaFor returns the grid's parsed creation form for kind, or nil
// when the plugin declares none. Since issue #209 the form opens on the
// FIRST DESCENT into a still-unconfigured tile (openConfigureTile) — the
// drop itself never prompts. A schema that fails to parse surfaces
// (charter §6: the plugin declared parameters we cannot render) and the
// tile stays inert until the plugin or schema is fixed.
func (a *App) createSchemaFor(gridID, kind string) (*schemaform.Form, bool) {
	g, ok := a.c.Grid(gridID)
	if !ok {
		return nil, true
	}
	schema, ok := g.Meta.CreateSchemas[kind]
	if !ok || schema == "" {
		return nil, true
	}
	form, err := schemaform.Parse(schema)
	if err != nil {
		a.reportErr(errsurface.Error, "schema", "cannot render the plugin's creation form: "+err.Error())
		return nil, false
	}
	return form, true
}
