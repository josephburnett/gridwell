package tileface

import gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

// BannerRuns is the banner's text in paint order: the name the server stamped
// and, after it, the owning plugin's status_detail — a word about the tile's
// state that nothing outside the plugin can derive. The shim paints status
// muted. A status is a note on a name, never a name: with no name there is no
// banner.
func BannerRuns(t *gridwellv1.Tile) (label, status string) {
	label = t.GetAltText()
	if label == "" {
		return "", ""
	}
	return label, t.GetStatusDetail()
}
