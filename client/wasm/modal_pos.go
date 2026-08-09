//go:build js && wasm

package main

import (
	"fmt"
	"syscall/js"

	"github.com/josephburnett/gridwell/client/panebox"
)

// centerCardOnActivePane positions a modal card over the ACTIVE pane's
// center (issue #251): the pane you acted in — clicked the menu, descended
// the unparameterized tile — is where the dialog appears, not the middle of
// the screen. The ONE centering rule for every modal card (the url modal
// and the instance picker): the geometry decision lives in
// panebox.ModalCardPos; this function only measures and applies styles.
//
// Call AFTER the modal is visible (the card must have layout to measure).
// The card is lifted out of the backdrop's flex centering by fixed
// positioning; margins are zeroed so the measured size is the placed size.
// With no laid-out focused pane (boot edge), the flex centering stays.
func (a *App) centerCardOnActivePane(card js.Value) {
	_, r, ok := a.focusedPaneRect()
	if !ok {
		return
	}
	w := card.Get("offsetWidth").Float()
	h := card.Get("offsetHeight").Float()
	win := js.Global()
	x, y := panebox.ModalCardPos(r, w, h,
		win.Get("innerWidth").Float(), win.Get("innerHeight").Float())
	st := card.Get("style")
	st.Set("position", "fixed")
	st.Set("margin", "0")
	st.Set("left", fmt.Sprintf("%.0fpx", x))
	st.Set("top", fmt.Sprintf("%.0fpx", y))
}
