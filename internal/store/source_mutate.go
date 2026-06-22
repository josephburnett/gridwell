package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// realHostActor is the production fs.Host wiring. File and directory
// removals go to the OS trash (recoverable) rather than os.Remove —
// deleting a file-well tile should not be an irreversible rm -rf.
type realHostActor struct{}

func (realHostActor) Remove(path string) error    { return trashHostPath(path) }
func (realHostActor) RemoveAll(path string) error { return trashHostPath(path) }

// trashHostPath moves path into the freedesktop home trash.
func trashHostPath(path string) error {
	dir, err := trashHomeDir()
	if err != nil {
		return err
	}
	return trashFileInto(dir, path)
}

// deleteSourceTile is the source-aware branch of DeleteTile. It delegates
// the host-side side effect (trash a file, signal a process) to the plugin
// and, when settled=true, immediately drops the tile row. When settled=false
// (proc SIGTERM is best-effort) the tile stays and the reconciler sweeps it
// once the process is definitively gone.
func (s *Store) deleteSourceTile(ctx context.Context, tx *sql.Tx, t *rpc.Tile, parent *rpc.Grid, events *[]rpc.Event) (handled bool, err error) {
	if t.SourceKey == "" {
		return false, fmt.Errorf("%w: source tile %d has no source_key", ErrInvalidArgument, t.ID)
	}
	src, ok := s.sources.Get(parent.SourceKind)
	if !ok {
		return false, nil
	}
	settled, err := src.Delete(ctx, parent.SourceID, t.SourceKey, t.Version)
	if err != nil {
		return true, err
	}
	if settled {
		if err := s.dropTileRow(ctx, tx, t, events); err != nil {
			return true, err
		}
	}
	return true, nil
}

// dropTileRow deletes a tile row and cleans up the refs it held (blob /
// child grid). Called from DeleteTile for regular tiles and from
// deleteSourceTile when the source confirms the artifact is gone (settled).
func (s *Store) dropTileRow(ctx context.Context, tx *sql.Tx, t *rpc.Tile, events *[]rpc.Event) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM tiles WHERE id = ?`, t.ID); err != nil {
		return err
	}
	childGridID, _ := strconv.ParseInt(t.ChildGridID, 10, 64)
	if err := s.decTileRefs(ctx, tx, t.Kind, childGridID, t.BlobID, t.PreviewBlobID); err != nil {
		return err
	}
	if err := s.bumpGridVersion(ctx, tx, t.GridID); err != nil {
		return err
	}
	*events = append(*events, rpc.Event{Kind: rpc.EventTileRemoved, TileRemoved: &rpc.TileRemoved{GridID: t.GridID, TileID: t.ID}})
	return nil
}

