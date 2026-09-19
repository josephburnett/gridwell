// Package barslot owns what the bottom bar's circle slot is for the focused
// pane. One verdict names both the affordance drawn and the action a click
// runs, so the button cannot promise one thing and do another. It is js-free;
// the shim keeps the input gathering, the pixels and the effect dispatch.
package barslot

// Mode names both a glyph and an action.
type Mode int

const (
	// ModeNothing covers a markdown descent, whose slot holds the DOM
	// text-mode toggle at the same center, an ephemeral shell visit, and a
	// frozen shell whose tmux session is gone.
	ModeNothing Mode = iota
	// ModeURLBack runs history.back().
	ModeURLBack
	// ModeGoLive reopens the descended tile: a url view placed, or a tmux
	// session created for a never-opened shell tile and attached. One glyph
	// and one meaning, so the caller dispatches on the tile's kind.
	ModeGoLive
	// ModeURLOpenTab opens the address in a new browser tab; the tile stays
	// frozen.
	ModeURLOpenTab
	// ModeFreeze is the screenshot: the live surface's face becomes the tile's
	// preview, the standing freeze lands on the row, and the surface closes.
	// Only a live shell reaches it — a live url's slot is its back button, so
	// that freeze rides the view's own context menu instead.
	ModeFreeze
	// ModePlus is the + menu toggle. The drawer swaps in the trashcan while a
	// tile drag is in flight.
	ModePlus
)

// Input is the world state the slot's mode reads. The caller resolves every
// field; nothing here is re-derived.
type Input struct {
	// Descent is pane.ContentID() != "".
	Descent bool
	// URLDescent is rpc.WebContent: a url tile or a serves_page tile.
	URLDescent   bool
	ShellDescent bool
	URLLive      bool
	ShellLive    bool
	// CanLiveURL is caps.LiveURL.
	CanLiveURL bool
	// ShellRefreshVisible is shellconn.DecideShellRefreshVisible's Show. Only
	// the frozen-shell arm reads it, and the caller must resolve it lazily
	// because resolving it kicks a liveness probe.
	ShellRefreshVisible bool
	// Durable is whether the descended row is one the pane's own grid holds.
	// An ephemeral visit is deleted on ascent, so there is nothing for a
	// standing freeze to be about.
	Durable bool
}

// Decide tests URLDescent before ShellDescent. The two cannot both be true, a
// shell tile not being web content, but the priority is fixed here rather than
// in each caller's arm order.
func Decide(in Input) Mode {
	if !in.Descent {
		return ModePlus
	}
	switch {
	case in.URLDescent:
		switch {
		case in.URLLive:
			return ModeURLBack
		case in.CanLiveURL:
			return ModeGoLive
		default:
			return ModeURLOpenTab
		}
	case in.ShellDescent:
		switch {
		case in.ShellLive:
			if in.Durable {
				return ModeFreeze
			}
		case in.ShellRefreshVisible:
			return ModeGoLive
		}
	}
	return ModeNothing
}
