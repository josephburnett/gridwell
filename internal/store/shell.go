package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// CreateShell creates a new shell tile. The bash session is NOT started
// here — matching the URL tile model, the tile begins frozen with an
// empty preview, and an explicit refresh from the client kicks off the
// PTY. shell_cwd is stamped to the server's $HOME so the first refresh
// has somewhere to start.
func (s *Store) CreateShell(ctx context.Context, req *rpc.CreateShellRequest) (*rpc.Tile, error) {
	cwd := os.Getenv("HOME")
	if cwd == "" {
		// Fall back to the gridwell process's cwd if HOME isn't set.
		// Some sandboxed environments unset it.
		if d, err := os.Getwd(); err == nil {
			cwd = d
		} else {
			cwd = "/"
		}
	}
	return s.createTile(ctx, req.Path, req.GridID, req.X, req.Y, req.W, req.H,
		func(tx *sql.Tx, gridID, now int64, objID string) (int64, error) {
			res, err := tx.ExecContext(ctx, `
				INSERT INTO tiles (object_id, grid_id, kind, x, y, w, h,
					shell_cwd, alt_text, created_at, updated_at)
				VALUES (?, ?, 'shell', ?, ?, ?, ?, ?, ?, ?, ?)`,
				objID, gridID, req.X, req.Y, req.W, req.H, cwd, "shell", now, now)
			if err != nil {
				return 0, fmt.Errorf("insert shell tile: %w", err)
			}
			return res.LastInsertId()
		})
}

// SetShellCwd persists the bash PID's last cwd before the session is
// killed. Called at ascent / shell-stream-close so a future refresh
// resumes in the same directory.
func (s *Store) SetShellCwd(ctx context.Context, req *rpc.SetShellCwdRequest) (*rpc.Tile, error) {
	var out *rpc.Tile
	err := s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		n, err := s.checkTileVersion(ctx, tx, req.TileID, req.Version)
		if err != nil {
			return err
		}
		if n.Kind != rpc.KindShell {
			return ErrNotShellTile
		}
		pre, err := s.preWrite(ctx, tx, req.Path, req.TileID)
		if err != nil {
			return err
		}
		*events = append(*events, pre.Events...)
		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET shell_cwd = ?, updated_at = ? WHERE id = ?`,
			req.ShellCwd, s.now().Unix(), pre.TargetTileID); err != nil {
			return err
		}
		if err := bumpTileVersion(ctx, tx, pre.TargetTileID); err != nil {
			return err
		}
		out, err = s.loadTile(ctx, tx, pre.TargetTileID)
		if err != nil {
			return err
		}
		*events = append(*events, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *out}})
		return nil
	})
	return out, err
}

// SetShellPreview overwrites the frozen-state JPEG. Bytes are hash-
// deduped through the blobs table the same way URL preview bytes are.
// Empty JPEG (length 0) clears the preview — useful as a reset after a
// failed refresh.
//
// Unlike most mutations this one intentionally does NOT consult
// req.Version. The preview is a side-channel content blob; the
// authoritative concurrency primitive for shell tiles is the WebSocket
// session (one live PTY per tile at a time). Without this relaxation,
// the freeze path races itself: the server's WS-close handler bumps
// the tile version via SetShellCwd before the client's HTTP
// SetShellPreview round-trip can land, and the version check would
// always fail. The field stays on the wire so clients can still
// observe versions through TileChanged.
func (s *Store) SetShellPreview(ctx context.Context, req *rpc.SetShellPreviewRequest) (*rpc.Tile, error) {
	var out *rpc.Tile
	err := s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		n, err := s.loadTile(ctx, tx, req.TileID)
		if err != nil {
			return err
		}
		if n.Kind != rpc.KindShell {
			return ErrNotShellTile
		}
		pre, err := s.preWrite(ctx, tx, req.Path, req.TileID)
		if err != nil {
			return err
		}
		*events = append(*events, pre.Events...)

		current, err := s.loadTile(ctx, tx, pre.TargetTileID)
		if err != nil {
			return err
		}
		oldBlobID := current.PreviewBlobID

		var newBlobID int64
		if len(req.JPEG) > 0 {
			hash := hashBytes(req.JPEG)
			newBlobID, err = putBlob(ctx, tx, hash, req.JPEG)
			if err != nil {
				return err
			}
		}
		var newArg any
		if newBlobID != 0 {
			newArg = newBlobID
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET preview_blob_id = ?, updated_at = ? WHERE id = ?`,
			newArg, s.now().Unix(), pre.TargetTileID); err != nil {
			return err
		}
		if oldBlobID != newBlobID {
			if newBlobID != 0 {
				if err := s.incBlobRefcount(ctx, tx, newBlobID); err != nil {
					return err
				}
			}
			if oldBlobID != 0 {
				if err := s.decBlobRefcount(ctx, tx, oldBlobID); err != nil {
					return err
				}
			}
		}
		if err := bumpTileVersion(ctx, tx, pre.TargetTileID); err != nil {
			return err
		}
		out, err = s.loadTile(ctx, tx, pre.TargetTileID)
		if err != nil {
			return err
		}
		*events = append(*events, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *out}})
		return nil
	})
	return out, err
}
