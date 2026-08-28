package node

// The native store (docs/v2-design.md §4.5, the v2 fold): the local
// plugin's content engine is NODE code — always in-process, never a
// subprocess. The server.yaml entry remains (it carries the durable
// uuid every stored reference is qualified by), but kind "home" no
// longer names a guest: it names the node's own store. Owner decision
// 2026-08-22 (content-presentation.md §9) — "the server holds no
// Gridwell state" was explicitly reversed.

import (
	"context"
	"fmt"
	"log"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/shellsvc"
	"github.com/josephburnett/gridwell/internal/local/tmux"
)

// NativeLocalFactory is the in-process constructor for the native store
// — the same wiring the gridwell-plugin-local main used to do: verified
// open, the per-DB private tmux server backing shell tiles, and the
// startup sweeps. A host without tmux degrades to no live shells (the
// node must come up; the failure surfaces on shell use), never to a
// dead node.
func NativeLocalFactory(cfg map[string]string) (gridwellv1.GridwellServer, error) {
	dbPath := cfg["db_file"]
	if dbPath == "" {
		return nil, fmt.Errorf("native local: db_file required")
	}
	uuid := cfg["uuid"]
	st, err := local.OpenVerified(dbPath, uuid, cfg["kind"])
	if err != nil {
		return nil, err
	}

	socket := "gridwell"
	if uuid != "" {
		socket = "gridwell-" + uuid
	}
	var mgr *shellsvc.Manager
	if ctrl, _, terr := tmux.New(socket, "", cfg["shell"]); terr != nil {
		log.Printf("gridwell: native local: tmux init: %v (live shells disabled on this host)", terr)
	} else {
		mgr = shellsvc.NewManager(shellsvc.NewLive(ctrl))
	}

	p := local.New(st, mgr)
	// Scratch tiles are ephemeral (deleted on ascent); this sweep is the
	// crash net. Before the orphan sweep, so a swept shell's session
	// reads as orphaned and gets killed there.
	if swept, err := p.CleanupScratch(context.Background()); err != nil {
		log.Printf("gridwell: native local: scratch cleanup: %v", err)
	} else if swept > 0 {
		log.Printf("gridwell: native local: scratch cleanup removed %d ephemeral tile(s)", swept)
	}
	if killed, err := p.CleanupOrphanedShells(context.Background()); err != nil {
		log.Printf("gridwell: native local: orphan cleanup: %v", err)
	} else if killed > 0 {
		log.Printf("gridwell: native local: orphan cleanup killed %d stale shell session(s)", killed)
	}
	return p, nil
}
