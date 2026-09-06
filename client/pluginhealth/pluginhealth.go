// Package pluginhealth decides how a launcher tile should draw and behave for
// a given rpc.PluginInfo: enterable, waiting (asked, no answer yet), or broken
// (anything that failed). It is the one place that decision is made, so
// client/wasm's launcher rendering and click handling are both thin reads of
// it; the wasm file contributes only pixels and event plumbing.
//
// Two non-enterable states, not three: the user does not care WHY a launcher
// will not open unless they are debugging, so every failure — Info errored,
// the probe timed out, Info answered and declared no root grid — is one
// status with one tint and one message shape, and the specific reason rides
// the click report's text. Still loading is the one genuinely different
// thing, and it presents as loading.
package pluginhealth

import (
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
)

// Status classifies a launcher plugin tile's interactivity.
type Status int

const (
	// Enterable: Info succeeded and declared a root grid. Click descends.
	Enterable Status = iota
	// Waiting: asked, not answered yet. A connection row is minted with no
	// root and no error until the far node answers, and the probe's timeout
	// is what ends the wait — a timed-out connection carries its detail as
	// InfoError and is Broken, never Waiting forever.
	Waiting
	// Broken: not working, whatever the reason. Info failed or timed out
	// (InfoError is set), or Info answered and declared no root grid to
	// enter. One status, because the difference is a debugging detail, not a
	// different thing to look at.
	Broken
)

// Classify decides pl's status from the facts the server's Info handshake
// produces: whether it failed (InfoError != ""), whether it declared a root
// (RootGridID != ""), and, for the rootless-and-errorless case, whether the
// row is a connection (rpc.PluginKindConnection, the row's own declaration,
// minted by rpc.ConnectionRow — never the shape of the uuid). A connection
// with no root has not answered yet; a plugin with no root has answered and
// said there is nothing to enter. This is the ONE classification: no caller
// asks a second question about the row afterwards.
func Classify(pl rpc.PluginInfo) Status {
	if pl.InfoError != "" {
		return Broken
	}
	if pl.RootGridID == "" {
		if pl.Kind == rpc.PluginKindConnection {
			return Waiting
		}
		return Broken
	}
	return Enterable
}

// UnrootedLink reports whether a tile is a well link with nothing behind it:
// a launcher row for a plugin or connection that declared no root grid. It is
// the drawn half of the same "not enterable" the descent guard reports on
// click, derived from the row alone, so it holds for a remote node's launcher
// rows too — the local plugin list cannot classify those, and Classify is
// only consulted when it can. It belongs beside Classify because both answer
// "is this enterable", from the two kinds of fact that can say no.
func UnrootedLink(t *rpc.Tile) bool {
	return t.Reference && rpc.IsWellKind(t.Kind) && t.ChildGridID == ""
}

// BrokenReason is the debugging detail behind a Broken status: what the server
// recorded, or, for the one failure that carries no error text, that the row
// declared no root. Only the click report reads it — the tint does not, since
// every Broken row looks the same.
func BrokenReason(pl rpc.PluginInfo) string {
	if pl.InfoError != "" {
		return pl.InfoError
	}
	return "no root configured — set config.root in server.yaml"
}

// ClickNotice returns the errsurface.Surface.Report arguments for clicking a
// non-enterable launcher tile: severity, a per-plugin source key (so a second
// click updates the same notice rather than scrolling the strip), and a
// human-readable message. ok is false for Enterable (the caller descends)
// — that click reports nothing.
//
// The source key is "launcher:<uuid>" — the uuid, not the label: two
// connections can share a label, and a shared source key would make one
// row's notice silently replace another's.
// The source namespace is "launcher:", not "plugin:" — "plugin:<uuid>" is the
// sticky ongoing-condition namespace (errsurface.Sticky) used by health
// events, whereas a click notice is a one-shot answer to a gesture and should
// expire off the strip like any other.
//
// Severity follows the status, not the reason: Broken is Error (something the
// user expected to work did not, and BrokenReason says what), Waiting is Info
// (nothing has gone wrong yet).
func ClickNotice(pl rpc.PluginInfo) (sev errsurface.Severity, source, message string, ok bool) {
	switch Classify(pl) {
	case Broken:
		return errsurface.Error, "launcher:" + pl.UUID, pl.Label + ": " + BrokenReason(pl), true
	case Waiting:
		return errsurface.Info, "launcher:" + pl.UUID, "loading " + pl.Label + " — it will open once the connection answers", true
	default:
		return 0, "", "", false
	}
}
