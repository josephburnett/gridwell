package node

// The node's home: its own content over its store, with the shell manager, a
// private per-node tmux server, and the boot sweeps. It is not a plugin; the
// node constructs it from its own config.

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
func newHome(st *store.Store, id, shell string) *local.Plugin {
	socket := "gridwell"
	if id != "" {
		socket = "gridwell-" + id
	}
	var p *local.Plugin
	if ctrl, _, terr := tmux.New(socket, "", shell); terr != nil {
		p = local.WithoutShells(st, fmt.Errorf("tmux init: %w", terr))
	} else {
		p = local.New(st, shellsvc.NewManager(shellsvc.NewLive(ctrl)))
	}
	// Scratch tiles are deleted on ascent; this sweep is the crash net. Before
	// the orphan sweep, so a swept shell's tmux reads as orphaned there.
	if swept, err := p.CleanupScratch(context.Background()); err != nil {
		log.Printf("gridwell: home: scratch cleanup: %v", err)
	} else if swept > 0 {
		log.Printf("gridwell: home: scratch cleanup removed %d ephemeral tile(s)", swept)
	}
	if killed, err := p.CleanupOrphanedShells(context.Background()); err != nil {
		log.Printf("gridwell: home: orphan cleanup: %v", err)
	} else if killed > 0 {
		log.Printf("gridwell: home: orphan cleanup killed %d stale tmux(es)", killed)
	}
	return p
}
