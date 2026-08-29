package node

// The node's HOME: its own content (text, urls, shells, wells, pane tiles)
// over its store, with the shell manager (tmux, a private per-node
// server) and the boot sweeps. Not a plugin: the node constructs it from
// its own config (docs/one-node.md).

import (
	"context"
	"log"

	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/shellsvc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/local/tmux"
)

// newHome builds the home over st. Shell tiles are tmux sessions on a
// private per-node socket (gridwell-<id>), so they survive restarts. A
// host without tmux degrades to no live shells (the node must come up;
// the failure surfaces on shell use), never to a dead node.
func newHome(st *store.Store, id, shell string) *local.Plugin {
	socket := "gridwell"
	if id != "" {
		socket = "gridwell-" + id
	}
	var mgr *shellsvc.Manager
	if ctrl, _, terr := tmux.New(socket, "", shell); terr != nil {
		log.Printf("gridwell: home: tmux init: %v (live shells disabled on this host)", terr)
	} else {
		mgr = shellsvc.NewManager(shellsvc.NewLive(ctrl))
	}
	p := local.New(st, mgr)
	// Scratch tiles are ephemeral (deleted on ascent); this sweep is the
	// crash net. Before the orphan sweep, so a swept shell's session
	// reads as orphaned and gets killed there.
	if swept, err := p.CleanupScratch(context.Background()); err != nil {
		log.Printf("gridwell: home: scratch cleanup: %v", err)
	} else if swept > 0 {
		log.Printf("gridwell: home: scratch cleanup removed %d ephemeral tile(s)", swept)
	}
	if killed, err := p.CleanupOrphanedShells(context.Background()); err != nil {
		log.Printf("gridwell: home: orphan cleanup: %v", err)
	} else if killed > 0 {
		log.Printf("gridwell: home: orphan cleanup killed %d stale shell session(s)", killed)
	}
	return p
}
