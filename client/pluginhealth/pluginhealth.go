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
	// (e.g. an fs plugin with no config.root). Healthy, just nothing to enter.
	Rootless
	// Parameterized: Info succeeded, no root grid, but an instance grid is
	// declared (issue #251 — e.g. ssh's connections). Healthy and clickable:
	// the click opens the instance picker instead of descending.
	Parameterized
)

// Classify decides pl's status from the facts the server's Info handshake
// produces: whether it failed (InfoError != ""), whether it declared a root
// (RootGridID != ""), and whether it declared an instance grid instead
// (InstanceGridID != "" — issue #251). InfoError is the ONLY signal that
// distinguishes Broken from the healthy rootless states — all three
// otherwise leave RootGridID == "".
func Classify(pl rpc.PluginInfo) Status {
	if pl.InfoError != "" {
		return Broken
	}
	if pl.RootGridID == "" {
		if pl.InstanceGridID != "" {
			return Parameterized
		}
		return Rootless
	}
	return Enterable
}

// ClickNotice returns the errsurface.Surface.Report arguments for clicking a
// non-enterable launcher tile: severity, a per-plugin source key (so a second
// click updates the same notice rather than scrolling the strip), and a
// human-readable message. ok is false for Enterable (the caller descends)
// and for Parameterized (the caller opens the instance picker, issue #251) —
// neither click reports anything.
//
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
		return errsurface.Error, "launcher:" + pl.Label, pl.Label + ": " + pl.InfoError, true
	case Rootless:
		return errsurface.Info, "launcher:" + pl.Label, pl.Label + " has no root configured — set config.root in server.yaml", true
	default:
		return 0, "", "", false
	}
}
