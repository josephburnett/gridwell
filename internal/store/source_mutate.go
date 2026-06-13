package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// HostActor is the surface for the destructive side-effects file-well /
// process-well deletions trigger: removing files / directories from the
// host FS, signalling host processes. Stubbable for tests so the
// reconciler tests don't actually rm anything.
type HostActor interface {
	Remove(path string) error
	RemoveAll(path string) error
	Kill(pid int64, sig syscall.Signal) error
}

// realHostActor is the production wiring: delegate to os and
// syscall.Kill.
type realHostActor struct{}

func (realHostActor) Remove(path string) error    { return os.Remove(path) }
func (realHostActor) RemoveAll(path string) error { return os.RemoveAll(path) }
func (realHostActor) Kill(pid int64, sig syscall.Signal) error {
	return syscall.Kill(int(pid), sig)
}

// SetHostActor overrides the actor used to apply host-side side effects
// of fs/proc-grid deletions. Production callers can leave this alone;
// tests inject a stub that records calls.
func (s *Store) SetHostActor(a HostActor) {
	if a != nil {
		s.host = a
	}
}

// sourceDeletePath computes the absolute path a fs-grid tile maps to.
// For sub-file-wells, that's the tile's fs_path directly. For file
// tiles (kind=text with source_key) it's the parent grid's source_id
// joined with the tile's source_key.
func sourceDeletePath(parentGrid *rpc.Grid, t *rpc.Tile) string {
	if t.Kind == rpc.KindFileWell {
		return t.FSPath
	}
	if parentGrid != nil && parentGrid.SourceKind == rpc.GridSourceFS && t.SourceKey != "" {
		return filepath.Join(parentGrid.SourceID, t.SourceKey)
	}
	return ""
}

// processWellTilePID returns the PID a deletion should target for a
// tile inside a proc-grid. Child process-well tiles map to their own
// PID; the synthetic @info tile maps to the well's own PID (which is
// the grid's source_id).
func processWellTilePID(parentGrid *rpc.Grid, t *rpc.Tile) int64 {
	if t.PID > 0 {
		return t.PID
	}
	if parentGrid != nil && parentGrid.SourceKind == rpc.GridSourceProc && t.SourceKey == "@info" {
		pid, _ := strconv.ParseInt(parentGrid.SourceID, 10, 64)
		return pid
	}
	return 0
}

// deleteSourceTile is the source-aware branch of DeleteTile. Removes the
// host-side artifact (file, directory, or process) the tile represents,
// then drops the tile row from the DB so the in-flight cache update is
// immediate. For process tiles we only signal — the tile reappears via
// reconcile if the process didn't actually die, which is the truthful
// outcome.
func (s *Store) deleteSourceTile(ctx context.Context, tx *sql.Tx, t *rpc.Tile, parent *rpc.Grid, events *[]rpc.Event) (handled bool, err error) {
	switch parent.SourceKind {
	case rpc.GridSourceFS:
		path := sourceDeletePath(parent, t)
		if path == "" {
			return false, fmt.Errorf("%w: fs tile %d has no path", ErrInvalidArgument, t.ID)
		}
		if t.Kind == rpc.KindFileWell {
			if err := s.host.RemoveAll(path); err != nil {
				return true, fmt.Errorf("rm -rf %s: %w", path, err)
			}
		} else {
			if err := s.host.Remove(path); err != nil {
				return true, fmt.Errorf("rm %s: %w", path, err)
			}
		}
		if err := s.dropTileRow(ctx, tx, t, events); err != nil {
			return true, err
		}
		return true, nil
	case rpc.GridSourceProc:
		pid := processWellTilePID(parent, t)
		if pid <= 0 {
			return false, fmt.Errorf("%w: proc tile %d has no pid", ErrInvalidArgument, t.ID)
		}
		if err := s.host.Kill(pid, syscall.SIGTERM); err != nil {
			return true, fmt.Errorf("kill %d: %w", pid, err)
		}
		// Don't drop the tile row — the reconciler will remove it on
		// next read if the process actually exited. Otherwise the
		// SIGTERM was ignored and the tile reasonably stays.
		return true, nil
	}
	return false, nil
}

// dropTileRow is the body of DeleteTile's "delete and clean up refs"
// step, factored out so deleteSourceTile can call it without
// duplicating the cleanup logic.
func (s *Store) dropTileRow(ctx context.Context, tx *sql.Tx, t *rpc.Tile, events *[]rpc.Event) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM tiles WHERE id = ?`, t.ID); err != nil {
		return err
	}
	if err := s.decTileRefs(ctx, tx, t.Kind, t.ChildGridID, t.BlobID, t.PreviewBlobID); err != nil {
		return err
	}
	if err := bumpGridVersion(ctx, tx, t.GridID); err != nil {
		return err
	}
	*events = append(*events, rpc.Event{Kind: rpc.EventTileRemoved, TileRemoved: &rpc.TileRemoved{GridID: t.GridID, TileID: t.ID}})
	return nil
}
