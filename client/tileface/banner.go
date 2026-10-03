package tileface

import gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"

// BannerText is the banner's one line: the owning plugin's status_detail (one
// emoji, sent only when there is something to notice) before the name the
// server stamped. The status leads because the banner clips from the right: a
// long name loses its tail, never the mark. A status is a note on a name,
// never a name: with no name there is no banner.
func BannerText(t *gridwellv1.Tile) string {
	label := t.GetAltText()
	status := t.GetStatusDetail()
	if label == "" || status == "" {
		return label
	}
	return status + " " + label
}
