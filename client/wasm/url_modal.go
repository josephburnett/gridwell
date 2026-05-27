//go:build js && wasm

package main

import (
	"syscall/js"
)

// openURLModal shows the URL-entry overlay and invokes onSubmit with the
// normalized URL once the user submits a valid value. onCancel fires if
// the user dismisses (Escape, cancel button, click-on-backdrop).
//
// Validation runs in-modal: invalid input shows an inline error and keeps
// the modal open. URLs without a scheme get `https://` prepended.
//
// Listeners are installed fresh on every open and released on close, so
// repeat opens don't leak js.FuncOf handles.
func (a *App) openURLModal(onSubmit func(url string), onCancel func()) {
	doc := a.doc
	modal := doc.Call("getElementById", "gw-url-modal")
	form := doc.Call("getElementById", "gw-url-form")
	input := doc.Call("getElementById", "gw-url-input")
	errEl := doc.Call("getElementById", "gw-url-error")
	cancelBtn := doc.Call("getElementById", "gw-url-cancel")

	input.Set("value", "")
	errEl.Set("textContent", "")
	modal.Get("classList").Call("add", "open")
	input.Call("focus")

	var (
		submitCb, cancelCb, keydownCb, backdropCb, inputCb js.Func
	)

	close := func() {
		modal.Get("classList").Call("remove", "open")
		form.Call("removeEventListener", "submit", submitCb)
		cancelBtn.Call("removeEventListener", "click", cancelCb)
		modal.Call("removeEventListener", "keydown", keydownCb)
		modal.Call("removeEventListener", "mousedown", backdropCb)
		input.Call("removeEventListener", "input", inputCb)
		submitCb.Release()
		cancelCb.Release()
		keydownCb.Release()
		backdropCb.Release()
		inputCb.Release()
		a.canvas.Call("focus")
	}

	commit := func() {
		raw := input.Get("value").String()
		url, err := normalizeURL(raw)
		if err != nil {
			errEl.Set("textContent", err.Error())
			input.Call("focus")
			input.Call("select")
			return
		}
		close()
		onSubmit(url)
	}

	cancel := func() {
		close()
		if onCancel != nil {
			onCancel()
		}
	}

	submitCb = js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) > 0 {
			args[0].Call("preventDefault")
		}
		commit()
		return nil
	})
	cancelCb = js.FuncOf(func(this js.Value, args []js.Value) any {
		cancel()
		return nil
	})
	keydownCb = js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		ev := args[0]
		// Swallow every key so the canvas's window-level keydown handler
		// (which forwards to descended URL tiles) does not also see it.
		ev.Call("stopPropagation")
		if ev.Get("key").String() == "Escape" {
			ev.Call("preventDefault")
			cancel()
		}
		return nil
	})
	backdropCb = js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		// Clicks on the dim backdrop dismiss; clicks inside the card don't.
		if args[0].Get("target").Equal(modal) {
			cancel()
		}
		return nil
	})
	inputCb = js.FuncOf(func(this js.Value, args []js.Value) any {
		// Clear stale errors as soon as the user starts editing.
		errEl.Set("textContent", "")
		return nil
	})

	form.Call("addEventListener", "submit", submitCb)
	cancelBtn.Call("addEventListener", "click", cancelCb)
	// Listen on the modal (not document) so we don't intercept other
	// keydowns while the modal is closed.
	modal.Call("addEventListener", "keydown", keydownCb)
	modal.Call("addEventListener", "mousedown", backdropCb)
	input.Call("addEventListener", "input", inputCb)
}

