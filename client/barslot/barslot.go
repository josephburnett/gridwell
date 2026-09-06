// Package barslot decides what the bottom bar's circle slot IS for the
// focused pane: the affordance it draws and the action a click on it runs,
// as one verdict.
//
// This is the single owner of that policy. The slot's mode — back, go-live,
// open-in-a-new-tab, shell-refresh, the + menu, or nothing — was spelled
// twice in the wasm shim, once by the drawer and once by the click
// dispatcher, in two hand-written switches whose arms were ordered
// differently. They agreed only because their predicates happen to be
// disjoint. Drawn affordance and click verdict now come from this one
// function, so the button cannot promise one thing and do another.
//
// The package is plain Go (no js/wasm build tag) so every arm is unit-tested
// headlessly; the shim keeps only the impure input gathering, the pixels,
// and the effect dispatch.
package barslot

// Mode is the one thing the slot is, for a given pane state. Each mode names
// both a glyph and an action: ModeURLGoLive and ModeShellRefresh draw the
// same refresh glyph but are separate modes, because a click on one places a
// url view and a click on the other attaches a PTY.
type Mode int

const (
	// ModeNothing: the slot is empty and a click on it does nothing. A
	// markdown descent, whose slot is occupied by the DOM text-mode toggle at
	// the same center (a canvas click reaching the slot there just missed it);
	// a live shell descent, which has no slot affordance; and a frozen shell
	// whose tmux session is gone, where the JPEG is all that remains.
	ModeNothing Mode = iota
	// ModeURLBack: a live url descent — the click is history.back() in the
	// native view.
	ModeURLBack
	// ModeURLGoLive: a frozen url descent on a host that can place a live
	// view — the click opens the url stream.
	ModeURLGoLive
	// ModeURLOpenTab: a frozen url descent on a host that cannot go live (a
	// plain browser, no Electron bridge) — the click opens the address in a
	// new browser tab, the browser host's next-best descent. The tile stays
	// frozen and untouched.
	ModeURLOpenTab
	// ModeShellRefresh: a frozen shell descent whose refresh button shows —
	// the click creates a fresh tmux session for a never-opened tile, or
	// attaches to the existing one.
	ModeShellRefresh
	// ModePlus: the pane is on a grid — the slot is the + menu toggle (and
	// the trashcan while a tile drag is in flight, which is the drawer's own
	// business).
	ModePlus
)

// Input is exactly the world state the slot's mode reads. Every field is a
// fact the caller resolves; nothing here is re-derived.
type Input struct {
	// Descent is whether the pane is descended into a tile at all
	// (pane.ContentID() != ""). False means the pane is showing a grid.
	Descent bool
	// URLDescent is whether that tile presents as web content — a url tile or
	// a serves_page tile, which engage identically (rpc.Tile.WebContent()).
	URLDescent bool
	// ShellDescent is whether that tile is a shell tile.
	ShellDescent bool
	// URLLive is whether a native url view is placed on this pane.
	URLLive bool
	// ShellLive is whether a PTY stream is attached to this pane.
	ShellLive bool
	// CanLiveURL is the host capability (caps.LiveURL): whether this host can
	// place a live url view at all.
	CanLiveURL bool
	// ShellRefreshVisible is shellconn.DecideShellRefreshVisible's Show for
	// the descended shell tile. Read only by the frozen-shell arm, so the
	// caller may resolve it lazily — and must, because resolving it kicks a
	// liveness probe.
	ShellRefreshVisible bool
}

// Decide maps a pane's state to its slot mode.
//
// URLDescent is tested before ShellDescent. The two cannot both be true — a
// shell descent resolves the pane's own grid row, and a shell tile is not web
// content — but the priority is fixed here rather than left to each caller's
// arm order, which is how the drawer and the click dispatcher came to
// disagree about it.
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
			return ModeURLGoLive
		default:
			return ModeURLOpenTab
		}
	case in.ShellDescent:
		if !in.ShellLive && in.ShellRefreshVisible {
			return ModeShellRefresh
		}
	}
	return ModeNothing
}
