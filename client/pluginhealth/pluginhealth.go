// Package pluginhealth decides how a launcher tile should draw and behave for
// a given rpc.PluginInfo: enterable, broken (Info failed/timed out), or
// rootless (Info succeeded but declared no root grid). Both non-enterable
// cases used to present identically — a normal-looking tile whose click
// silently did nothing (input.go's enterPlugin bailed at RootGridID == "").
// This package is the one place that decision is made, so client/wasm's
// launcher rendering and click handling can both be thin reads of it
// (charter §5: decision logic in a js-free package, unit-tested; the wasm
// file contributes only pixels and event plumbing).
package pluginhealth

import (
	"strings"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
)

// Status classifies a launcher plugin tile's interactivity.
type Status int

const (
	// Enterable: Info succeeded and declared a root grid. Click descends.
	Enterable Status = iota
	// Broken: Info failed or timed out (InfoError is set). The plugin is
	// crashed, hung, or otherwise not responding.
	Broken
	// Rootless: Info succeeded, but the plugin has no root grid configured
	// (e.g. an fs plugin with no config.root). Healthy, just nothing to
	// enter. (The Parameterized status — an instance grid instead of a
	// root, #251 — retired 2026-08-23 with the instance picker: a
	// parameterized plugin's instances present as rows of their own, and
	// its bare row, listed only when its instance grid is unreadable,
	// reads Rootless.)
	Rootless
)

// Classify decides pl's status from the facts the server's Info handshake
// produces: whether it failed (InfoError != "") and whether it declared a
// root (RootGridID != ""). InfoError is the ONLY signal that distinguishes
// Broken from the healthy rootless state — both otherwise leave
// RootGridID == "".
func Classify(pl rpc.PluginInfo) Status {
	if pl.InfoError != "" {
		return Broken
	}
	if pl.RootGridID == "" {
		return Rootless
	}
	return Enterable
}

// ClickNotice returns the errsurface.Surface.Report arguments for clicking a
// non-enterable launcher tile: severity, a per-plugin source key (so a second
// click updates the same notice rather than scrolling the strip), and a
// human-readable message. ok is false for Enterable (the caller descends)
// — that click reports nothing.
//
// The source key is "launcher:<uuid>" — the UUID, not the label: two
// connections can share a label, and a shared source key would make one
// row's notice silently replace another's (found 2026-08-23).
// The source namespace is "launcher:", not "plugin:" — "plugin:<uuid>" is the
// sticky ongoing-condition namespace (errsurface.Sticky) used by health
// events, whereas a click notice is a one-shot answer to a gesture and should
// expire off the strip like any other.
//
// Severity: Broken is Error (something the user expected to work did not);
// Rootless is Info (an expected, fixable configuration gap, not a failure).
func ClickNotice(pl rpc.PluginInfo) (sev errsurface.Severity, source, message string, ok bool) {
	switch Classify(pl) {
	case Broken:
		return errsurface.Error, "launcher:" + pl.UUID, pl.Label + ": " + pl.InfoError, true
	case Rootless:
		// A chained uuid is a CONNECTION row (v2 #269): rootless means
		// its remote hasn't answered yet — a waiting state, not a
		// config gap.
		if strings.Contains(pl.UUID, "/") {
			return errsurface.Info, "launcher:" + pl.UUID, pl.Label + " — the remote hasn't answered yet; it will open once the connection comes up", true
		}
		return errsurface.Info, "launcher:" + pl.UUID, pl.Label + " has no root configured — set config.root in server.yaml", true
	default:
		return 0, "", "", false
	}
}
