// Package deadref decides whether a link is dead: its path ends in nothing,
// at a hop that does not declare the next namespace or at a target its
// namespace says is gone. Such a link is greyed, nothing said about it. The
// first hop's declaration is judged from the handshake roster, read fresh;
// everything else only the owning node can judge, and its answer is the dead
// verdict a read heard. A declared namespace that is down is not dead, but
// pluginhealth's and cache.SourceDark's.
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

// DeadTile reports that a link is dead: its target's namespace is undeclared
// here, or answered is true for its target, meaning a read heard the dead
// verdict from whichever hop broke (gwerr.IsDeadRef). answered may be nil.
func DeadTile(t *gridwellv1.Tile, rows []*gridwellv1.PluginInfo, nodeID string, answered func(id string) bool) bool {
	id := TargetID(t)
	if id != "" && answered != nil && answered(id) {
		return true
	}
	return Dead(id, rows, nodeID)
}
