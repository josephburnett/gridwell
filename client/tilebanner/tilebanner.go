// Package tilebanner owns what a tile's banner reads: the name the server
// stamped and, after it, the owning plugin's status_detail — a word about the
// tile's state ("unread", "done") that nothing outside the plugin can derive.
package tilebanner

import gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

// Runs is the banner's text in paint order; the shim paints status muted. A
// status is a note on a name, never a name: with no name there is no banner.
func Runs(t *gridwellv1.Tile) (label, status string) {
	label = t.GetAltText()
	if label == "" {
		return "", ""
	}
	return label, t.GetStatusDetail()
}
