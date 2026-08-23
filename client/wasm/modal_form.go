//go:build js && wasm

package main

// The shared modal-form infrastructure: one fixed overlay container and
// the js.Func bookkeeping for whatever form currently owns it. Born as
// the instance picker's plumbing (#251); the picker died with the v2
// config-managed connections (2026-08-23), and the entry-params form
// (#258 — the surviving parameterized-thing dialog) inherits it.

import (
	"syscall/js"

	"github.com/josephburnett/gridwell/client/schemaform"
)

// buildSchemaFieldRows appends one labeled input row per schema field to
// card and returns the inputs by field name — the one rendering of a
// params schema (the entry-params form, #258, is its consumer).
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

// instPickerEl lazily creates the fixed overlay container the modal
// forms render into. The id stays gw-inst-picker: the e2e drivers and
// the live-view parking predicate key on it.
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

// pickerFunc registers a js callback owned by the current modal render.
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
