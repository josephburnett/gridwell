package node

// The node's home: its own content over its store, with the shell manager, a
// private per-node tmux server, and the boot sweeps, constructed
// from the node's own config.

import (
	"context"
	"fmt"
	"log"

	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/shellsvc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/local/tmux"
)

// newHome builds the home over st. Shell tiles are tmuxes on a private
// per-node socket (gridwell-<id>), so they survive restarts. A host without
// tmux comes up with no live shells, never as a dead node, and a shell opened
// there is refused with why.
func newHome(st *store.Store, id, shell string) *local.Home {
	socket := "gridwell"
	if id != "" {
		socket = "gridwell-" + id
	}
	var p *local.Home
	if ctrl, _, terr := tmux.New(socket, "", shell); terr != nil {
		p = local.WithoutShells(st, fmt.Errorf("tmux init: %w", terr))
	} else {
		p = local.New(st, shellsvc.NewManager(shellsvc.NewLive(ctrl)))
	}
	// A visit is closed into the trash when its pane leaves it; this sweep is
	// the crash net.
	if swept, err := p.CleanupScratch(context.Background()); err != nil {
		log.Printf("gridwell: home: scratch cleanup: %v", err)
	} else if swept > 0 {
		log.Printf("gridwell: home: scratch cleanup moved %d ephemeral tile(s) to the trash", swept)
	}
	if killed, err := p.CleanupOrphanedShells(context.Background()); err != nil {
		log.Printf("gridwell: home: orphan cleanup: %v", err)
	} else if killed > 0 {
		log.Printf("gridwell: home: orphan cleanup killed %d stale tmux(es)", killed)
	}
	return p
}
