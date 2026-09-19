// Package deadref decides whether a link points into a namespace this node
// does not declare. Such a link is dead: greyed, never fetched, nothing said
// about it. A declared namespace that is down is not dead, but pluginhealth's
// and cache.SourceDark's. The verdict is the handshake roster's, read fresh.
package deadref

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// TargetID is the qualified id a link tile points at. The node-derived
// reference bit is the authoritative "is a link" bit.
func TargetID(t *gridwellv1.Tile) string {
	if t == nil || !t.Reference {
		return ""
	}
	if t.LinkTargetId != "" {
		return t.LinkTargetId
	}
	// A childless reference is a menu swatch or doorway tile, which names no
	// namespace and stays pluginhealth's.
	return t.ChildGridId
}

// Dead reports that id names a namespace absent from rows, the handshake's
// plugins. It
// answers false whenever it cannot know: a bare id, an empty roster, or a
// chain through a declared connection, the far node's to judge.
func Dead(id string, rows []*gridwellv1.PluginInfo, nodeID string) bool {
	if id == "" || len(rows) == 0 {
		return false
	}
	ns := rpc.OwnerNamespaceOf(id, nodeID)
	if ns == "" {
		return false
	}
	for i := range rows {
		if rows[i].Uuid == ns {
			return false
		}
	}
	return true
}

func DeadTile(t *gridwellv1.Tile, rows []*gridwellv1.PluginInfo, nodeID string) bool {
	return Dead(TargetID(t), rows, nodeID)
}
