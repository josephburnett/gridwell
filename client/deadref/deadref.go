// Package deadref decides one thing: is a link pointing into a namespace
// this node does not declare?
//
// A link tile stores a qualified id into another namespace — a plugin, a
// connection, or the node's own home. Remove that plugin from server.yaml,
// remove or retire that connection, and the id keeps naming something the
// node will not route. The link is DEAD: not late, not unreachable, but
// addressed to a namespace that is not there.
//
// Dead is not dark. A declared plugin whose subprocess is down and a
// declared connection whose remote will not answer are both still declared;
// they come back, and their state is the health and stale machinery's
// (pluginhealth, Grid.Meta.Stale). Only a namespace the node does not
// declare at all is dead.
//
// Dead is not always forever either, and this decides nothing about that: a
// retired connection name never returns, while a namespace the config merely
// stopped declaring is dead only while it is undeclared and live again with
// its declaration. Either way the verdict is the roster's, read fresh.
//
// The verdict reads the node's own declaration — the handshake roster the
// + menu is built from — and never an error. It asks the same peel the
// router routes by (rpc.OwnerNamespaceOf), so client and node cannot
// disagree about which namespace an id names. That is why it needs no
// failed fetch to reach it: a dead link is never asked for, so it costs
// nothing and surfaces nothing.
package deadref

import "github.com/josephburnett/gridwell/api/rpc"

// TargetID returns the qualified id a link tile points at, "" when the tile
// is not a link or names no target. The two link shapes are a well's
// qualified child grid and a leaf's link target; rpc.Tile.Reference is the
// one authoritative "is a link" bit, derived by the node, and this reads it
// rather than guessing from the ids.
func TargetID(t *rpc.Tile) string {
	if t == nil || !t.Reference {
		return ""
	}
	if t.LinkTargetID != "" {
		return t.LinkTargetID
	}
	// A childless reference is a menu swatch or an unrooted launcher tile,
	// not a link into anywhere: it names no namespace, so it has no verdict
	// here and pluginhealth keeps owning it.
	return t.ChildGridID
}

// Dead reports that id names a namespace the node does not declare. rows is
// the node's declared namespaces as the handshake gives them — its plugins
// followed by its connections (rpc.MenuRows) — and nodeID is the node's own
// id, the segment its home and its connections hang under.
//
// It answers false whenever it cannot know: a bare id, an empty roster (the
// handshake has not landed, and everything would look dead), or a chain
// through a declared connection, whose deeper segments name the FAR node's
// namespaces and are that node's to judge, not this one's.
func Dead(id string, rows []rpc.PluginInfo, nodeID string) bool {
	if id == "" || len(rows) == 0 {
		return false
	}
	ns := rpc.OwnerNamespaceOf(id, nodeID)
	if ns == "" {
		return false
	}
	for i := range rows {
		if rows[i].UUID == ns {
			return false
		}
	}
	return true
}

// DeadTile reports that t is a link into a namespace the node does not
// declare — the tile the client draws greyed and never fetches for.
func DeadTile(t *rpc.Tile, rows []rpc.PluginInfo, nodeID string) bool {
	return Dead(TargetID(t), rows, nodeID)
}
