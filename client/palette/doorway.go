package palette

// Which swatches the + menu's top section shows: one per declared doorway. A
// node is a place, so its home and a connection's far home each get a swatch.
// A plugin is not a place; it contributes one swatch per collection, and none
// with no collections. A row that declares no doorway is a swatch only when it
// is broken or still waiting on an answer. client/door decides what a doorway
// is and what it is called.

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/client/door"
	"github.com/josephburnett/gridwell/client/pluginhealth"
)

// Doorways composes the section in handshake order, each row's declared
// entries directly after it.
func Doorways(rows []*gridwellv1.PluginInfo) []door.Place {
	out := make([]door.Place, 0, len(rows))
	for i := range rows {
		row := rows[i]
		places := door.PlacesOf(row)
		out = append(out, places...)
		if _, classified := pluginhealth.Classify(row); len(places) == 0 && classified {
			out = append(out, door.Place{Plugin: row})
		}
	}
	return out
}
