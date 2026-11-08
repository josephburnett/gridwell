package menu

// Which swatches the + menu's top section shows: one per declared door. A
// node is a place, so its home and a connection's far home each get a swatch.
// The files are not a place; they contribute one swatch per root, and none
// with no roots. A row that declares no door is a swatch only when it
// is broken or still waiting on an answer. client/door decides what a door
// is and what it is called.

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/client/door"
	"github.com/josephburnett/gridwell/client/sourcehealth"
)

// Doors composes the section in handshake order, each row's grids directly
// after it.
func Doors(rows []*gridwellv1.SourceInfo) []door.Place {
	out := make([]door.Place, 0, len(rows))
	for i := range rows {
		row := rows[i]
		places := door.PlacesOf(row)
		out = append(out, places...)
		if _, classified := sourcehealth.Classify(row); len(places) == 0 && classified {
			out = append(out, door.Place{Row: row})
		}
	}
	return out
}
