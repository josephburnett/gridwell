// Package barslot owns what the bottom bar's circle slot is for the focused
// pane. One verdict names both the affordance drawn and the action a click
// runs, so the button cannot promise one thing and do another. It is js-free;
// the shim keeps the input gathering, the pixels and the effect dispatch.
package barslot

import "github.com/josephburnett/gridwell/api/rpc"

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
	// Descent is pane.ContentID() != "": the pane is inside a tile, of
	// whatever kind.
	Descent bool
	// Content is rpc.DescentOf for that tile. It is one classification, so no
	// pane is both a url descent and a shell descent.
	Content   rpc.Descent
	URLLive   bool
	ShellLive bool
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

// String is the mode's name, the one spelling of it outside this package.
func (m Mode) String() string {
	switch m {
	case ModeURLBack:
		return "back"
	case ModeGoLive:
		return "golive"
	case ModeURLOpenTab:
		return "opentab"
	case ModeFreeze:
		return "freeze"
	case ModePlus:
		return "plus"
	}
	return "nothing"
}

// Decide dispatches on the content family the pane is descended into.
func Decide(in Input) Mode {
	if !in.Descent {
		return ModePlus
	}
	switch in.Content {
	case rpc.DescentURL:
		switch {
		case in.URLLive:
			return ModeURLBack
		case in.CanLiveURL:
			return ModeGoLive
		default:
			return ModeURLOpenTab
		}
	case rpc.DescentShell:
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
