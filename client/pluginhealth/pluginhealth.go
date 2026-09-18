// Package pluginhealth owns how a launcher tile draws and behaves. Every
// failure is one status, Broken, with the reason only in the click report,
// since the user cannot act on which failure it was. Declaring no grid of its
// own is not a failure but NoDoor: a plugin is not itself a place.
package pluginhealth

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/errsurface"
)

// Status classifies a launcher plugin tile's interactivity.
type Status int

const (
	Enterable Status = iota // Info succeeded and declared a root grid
	// Waiting is a connection row minted with no root and no error, before
	// the far node answers. The probe's timeout ends the wait as Broken, so
	// nothing waits forever.
	Waiting
	Broken // InfoError is set
	NoDoor // answered, no grid of its own: nothing is wrong, nothing is shown
)

// Classify decides pl's status from the Info handshake alone, and no caller
// asks a second question about the row. Whether a rootless, errorless row is a
// connection comes from its declared Kind, never from the uuid's shape.
func Classify(pl *gridwellv1.PluginInfo) Status {
	if pl.InfoError != "" {
		return Broken
	}
	if pl.RootGridId == "" {
		if rpc.IsConnectionRow(pl) {
			return Waiting
		}
		return NoDoor
	}
	return Enterable
}

// UnrootedLink is a well link with no root grid behind it. It reads the tile
// alone, so it holds for a remote node's launcher rows too, which the local
// plugin list cannot classify.
func UnrootedLink(t *gridwellv1.Tile) bool {
	return t.Reference && rpc.IsWellKind(t.Kind) && t.ChildGridId == ""
}

// BrokenReason is what the server recorded. Only the click report reads it;
// every Broken row draws the same.
func BrokenReason(pl *gridwellv1.PluginInfo) string { return pl.InfoError }

// ClickNotice is errsurface.Surface.Report's arguments for clicking a
// non-enterable launcher tile; false for Enterable, which descends instead.
// The source keys on the uuid because two connections can share a label, and
// on "launcher:" because "plugin:" is errsurface's sticky namespace and a
// click notice should expire.
func ClickNotice(pl *gridwellv1.PluginInfo) (sev errsurface.Severity, source, message string, ok bool) {
	switch Classify(pl) {
	case Broken:
		return errsurface.Error, "launcher:" + pl.Uuid, pl.Label + ": " + BrokenReason(pl), true
	case Waiting:
		return errsurface.Info, "launcher:" + pl.Uuid, "loading " + pl.Label + " — it will open once the connection answers", true
	default:
		return 0, "", "", false
	}
}
