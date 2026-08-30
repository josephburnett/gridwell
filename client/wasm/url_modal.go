//go:build js && wasm

package main

import (
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/urlnorm"
)

// maxURLSuggestions caps the autocomplete dropdown — enough to be useful,
// short enough to stay a glanceable list rather than a scroll.
const maxURLSuggestions = 8

// urlSuggestCandidates collects the address and captured page title of every
// cached url tile in the given plugin, for the new-url modal's autocomplete.
// It is scoped to one plugin, so you autocomplete only from url tiles in the
// space you are creating the tile in, and bound to the cache, so grids not
// opened this session do not contribute.
func (a *App) urlSuggestCandidates(pluginUUID string) []urlnorm.Candidate {
	var out []urlnorm.Candidate
	a.forEachCachedGrid(func(gid string, g *cache.Grid) bool {
		if uuidOf(gid) != pluginUUID {
			return true
		}
		for _, t := range g.Tiles {
			if t.Kind == rpc.KindURL && t.URLString != "" {
				// AltText is the page title captured at freeze, so typing
				// title words finds the address.
				out = append(out, urlnorm.Candidate{URL: t.URLString, Title: t.AltText})
			}
		}
		return true
	})
	return out
}

// openURLModal shows the URL-entry overlay and invokes onSubmit with the
// normalized URL once the user submits a valid value. onCancel fires if
// the user dismisses (Escape, cancel button, click-on-backdrop).
//
// candidates are url-tile addresses (from urlSuggestCandidates) offered as
// autocomplete: the input filters them via urlnorm.Suggest, the arrow keys move
// a highlight, and Enter / click fills and submits the highlighted one.
//
// Validation runs in-modal: invalid input shows an inline error and keeps
// the modal open. URLs without a scheme get `https://` prepended.
//
// Listeners are installed fresh on every open and released on close, so
// repeat opens don't leak js.FuncOf handles.
func (a *App) openURLModal(candidates []urlnorm.Candidate, onSubmit func(url string), onCancel func()) {
	if a.urlModalOpen {
		return
	}
	a.urlModalOpen = true
	// Park live views now (liveOverlaysHidden) so none paints over the
	// dialog.
	a.draw()

	doc := a.doc
	modal := doc.Call("getElementById", "gw-url-modal")
	form := doc.Call("getElementById", "gw-url-form")
	input := doc.Call("getElementById", "gw-url-input")
	errEl := doc.Call("getElementById", "gw-url-error")
	cancelBtn := doc.Call("getElementById", "gw-url-cancel")
	suggestEl := doc.Call("getElementById", "gw-url-suggest")

	// Suggestion state shared by the input, keydown, and click handlers.
	var suggestions []urlnorm.Candidate
	activeIdx := -1 // -1 = the typed input itself (no highlight)

	renderSuggest := func() {
		suggestEl.Set("innerHTML", "")
		for i, s := range suggestions {
			li := doc.Call("createElement", "li")
			// One text node (no child spans): the mousedown handler reads
			// dataset.url off its target, which must stay the li itself.
			label := s.URL
			if s.Title != "" {
				label = s.Title + " — " + s.URL
			}
			li.Set("textContent", label)
			li.Get("dataset").Set("url", s.URL)
			if i == activeIdx {
				li.Get("classList").Call("add", "active")
			}
			suggestEl.Call("appendChild", li)
		}
	}
	refreshSuggest := func() {
		suggestions = urlnorm.Suggest(input.Get("value").String(), candidates, maxURLSuggestions)
		activeIdx = -1
		renderSuggest()
	}

	input.Set("value", "")
	errEl.Set("textContent", "")
	modal.Get("classList").Call("add", "open")
	a.centerCardOnActivePane(form) // the card IS the form element
	input.Call("focus")
	refreshSuggest() // empty input → most-recent few, so a pick is one key away

	var (
		submitCb, cancelCb, keydownCb, backdropCb, inputCb, suggestCb js.Func
	)

	close := func() {
		a.urlModalOpen = false
		defer a.draw() // un-park the live views (issue #131)
		modal.Get("classList").Call("remove", "open")
		suggestEl.Set("innerHTML", "")
		form.Call("removeEventListener", "submit", submitCb)
		cancelBtn.Call("removeEventListener", "click", cancelCb)
		modal.Call("removeEventListener", "keydown", keydownCb)
		modal.Call("removeEventListener", "mousedown", backdropCb)
		input.Call("removeEventListener", "input", inputCb)
		suggestEl.Call("removeEventListener", "mousedown", suggestCb)
		submitCb.Release()
		cancelCb.Release()
		keydownCb.Release()
		backdropCb.Release()
		inputCb.Release()
		suggestCb.Release()
		a.canvas.Call("focus")
	}

	commit := func() {
		raw := input.Get("value").String()
		url, err := urlnorm.Normalize(raw)
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
		switch ev.Get("key").String() {
		case "Escape":
			ev.Call("preventDefault")
			cancel()
		case "ArrowDown":
			if len(suggestions) > 0 {
				ev.Call("preventDefault")
				if activeIdx < len(suggestions)-1 {
					activeIdx++
				}
				renderSuggest()
			}
		case "ArrowUp":
			if len(suggestions) > 0 {
				ev.Call("preventDefault")
				if activeIdx > -1 {
					activeIdx--
				}
				renderSuggest()
			}
		case "Enter":
			// Fill the highlighted suggestion, then let the form's submit
			// fire and commit the chosen value (no preventDefault here).
			if activeIdx >= 0 && activeIdx < len(suggestions) {
				input.Set("value", suggestions[activeIdx].URL)
			}
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
		// Clear stale errors as soon as the user starts editing, and refresh
		// the autocomplete list for the new input.
		errEl.Set("textContent", "")
		refreshSuggest()
		return nil
	})
	suggestCb = js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		ev := args[0]
		// mousedown, not click, so this fires before the input blurs;
		// preventDefault keeps focus in the input. The li carries its url in
		// data-url.
		url := ev.Get("target").Get("dataset").Get("url")
		if url.Type() != js.TypeString || url.String() == "" {
			return nil
		}
		ev.Call("preventDefault")
		input.Set("value", url.String())
		commit()
		return nil
	})

	form.Call("addEventListener", "submit", submitCb)
	cancelBtn.Call("addEventListener", "click", cancelCb)
	// Listen on the modal rather than the document, so this intercepts no
	// keydowns while the modal is closed.
	modal.Call("addEventListener", "keydown", keydownCb)
	modal.Call("addEventListener", "mousedown", backdropCb)
	input.Call("addEventListener", "input", inputCb)
	suggestEl.Call("addEventListener", "mousedown", suggestCb)
}
