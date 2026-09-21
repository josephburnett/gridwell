//go:build js && wasm

package main

import (
	"syscall/js"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/urlnorm"
)

// maxURLSuggestions keeps the autocomplete a glanceable list rather than a
// scroll.
const maxURLSuggestions = 8

// urlSuggestCandidates collects the address and captured title of every
// cached url tile in the given plugin. Scoped to one plugin, so autocomplete
// offers only the space you are creating in, and bound to the cache, so grids
// not opened this session do not contribute.
func (a *App) urlSuggestCandidates(pluginUUID string) []urlnorm.Candidate {
	var out []urlnorm.Candidate
	a.forEachCachedGrid(func(gid string, g *cache.Grid) bool {
		if uuidOf(gid) != pluginUUID {
			return true
		}
		for _, t := range g.Tiles {
			if t.Kind == rpc.KindURL && t.UrlString != "" {
				// AltText is the title captured at freeze, so typing title
				// words finds the address.
				out = append(out, urlnorm.Candidate{URL: t.UrlString, Title: t.AltText})
			}
		}
		return true
	})
	return out
}

// openURLModal shows the URL-entry overlay and invokes onSubmit with the
// normalized URL. onCancel fires on a dismissal. Validation runs in-modal, so
// invalid input shows an inline error and keeps the modal open.
func (a *App) openURLModal(candidates []urlnorm.Candidate, onSubmit func(url string), onCancel func()) {
	if a.overlays.urlModalOpen {
		return
	}
	a.overlays.urlModalOpen = true
	// Park live views so none paints over the dialog.
	a.draw()

	doc := a.doc
	modal := doc.Call("getElementById", "gw-url-modal")
	form := doc.Call("getElementById", "gw-url-form")
	input := doc.Call("getElementById", "gw-url-input")
	errEl := doc.Call("getElementById", "gw-url-error")
	cancelBtn := doc.Call("getElementById", "gw-url-cancel")
	suggestEl := doc.Call("getElementById", "gw-url-suggest")

	var suggestions []urlnorm.Candidate
	activeIdx := -1 // the typed input itself, no highlight

	renderSuggest := func() {
		suggestEl.Set("innerHTML", "")
		for i, s := range suggestions {
			li := doc.Call("createElement", "li")
			// One text node and no child spans, because the mousedown
			// handler reads dataset.url off its target.
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
	a.centerCardOnActivePane(form) // the card is the form element
	input.Call("focus")
	refreshSuggest() // empty input lists a few, so a pick is one key away

	var offs []func()

	close := func() {
		a.overlays.urlModalOpen = false
		defer a.draw() // un-park the live views
		modal.Get("classList").Call("remove", "open")
		suggestEl.Set("innerHTML", "")
		for _, off := range offs {
			off()
		}
		offs = nil
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

	offs = []func(){
		listen(form, "submit", func(ev js.Value) {
			ev.Call("preventDefault")
			commit()
		}),
		listen(cancelBtn, "click", func(js.Value) { cancel() }),
		// On the modal rather than the document, so this intercepts no keydowns
		// while the modal is closed.
		listen(modal, "keydown", func(ev js.Value) {
			// Swallow every key, so the canvas's window-level keydown handler
			// does not also see it.
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
				// Fill the highlighted suggestion and let the form's submit fire,
				// so no preventDefault here.
				if activeIdx >= 0 && activeIdx < len(suggestions) {
					input.Set("value", suggestions[activeIdx].URL)
				}
			}
		}),
		listen(modal, "mousedown", func(ev js.Value) {
			// Clicks on the dim backdrop dismiss; clicks inside the card do not.
			if ev.Get("target").Equal(modal) {
				cancel()
			}
		}),
		listen(input, "input", func(js.Value) {
			// Clear stale errors as soon as the user edits.
			errEl.Set("textContent", "")
			refreshSuggest()
		}),
		listen(suggestEl, "mousedown", func(ev js.Value) {
			// mousedown, not click, so this fires before the input blurs, and
			// preventDefault keeps focus in the input.
			url := ev.Get("target").Get("dataset").Get("url")
			if url.Type() != js.TypeString || url.String() == "" {
				return
			}
			ev.Call("preventDefault")
			input.Set("value", url.String())
			commit()
		}),
	}
}
