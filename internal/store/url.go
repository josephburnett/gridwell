package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// URLDriver is the abstraction the store uses for URL-tile presence.
// The production implementation drives headless Chromium via CDP and
// owns one Session per /rpc/URLStream WebSocket; the store only needs
// to know whether the driver is functional.
type URLDriver interface {
	// Available reports whether the driver is functional (e.g. the
	// Chromium binary was found at startup). When false, callers that
	// need a live tab return ErrChromiumUnavailable.
	Available() bool
}

// FakeURLDriver is an in-memory URLDriver for tests.
type FakeURLDriver struct {
	mu        sync.Mutex
	available bool
}

// NewFakeURLDriver returns a FakeURLDriver with Available = true.
func NewFakeURLDriver() *FakeURLDriver {
	return &FakeURLDriver{available: true}
}

// SetAvailable changes the driver's Available flag.
func (d *FakeURLDriver) SetAvailable(v bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.available = v
}

func (d *FakeURLDriver) Available() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.available
}

// ForkURL duplicates a URL tile as a frozen sibling in the destination
// grid at (X, Y). The new tile carries the source's current URL and
// preview JPEG and is born dormant. Source and destination grids may be
// the same or different; both require write permission. The source
// tile's liveness is unchanged.
func (s *Store) ForkURL(ctx context.Context, userID int64, req *rpc.ForkURLRequest) (*rpc.Tile, error) {
	var out *rpc.Tile
	var events []rpc.Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		src, err := s.loadTile(ctx, tx, req.TileID)
		if err != nil {
			return err
		}
		if !src.IsURL() {
			return ErrNotURLTile
		}
		// Source-side locality.
		if !req.ViewRect.Intersects(src.X, src.Y, src.W, src.H) {
			return ErrLocality
		}
		// Source-side perm: read is enough since we're not modifying it.
		read, _, err := s.permForTile(ctx, tx, userID, req.TileID)
		if err != nil {
			return err
		}
		if !read {
			return ErrPermissionDenied
		}
		// Source-side path validity.
		if _, err := s.buildGridSequence(ctx, tx, userID, req.Path); err != nil {
			return err
		}

		// Dest-side validation.
		dstSeq, err := s.buildGridSequence(ctx, tx, userID, req.DestPath)
		if err != nil {
			return err
		}
		if dstSeq.grids[len(dstSeq.grids)-1] != req.DestGridID {
			return fmt.Errorf("%w: dest path leaf mismatch", ErrInvalidPath)
		}
		_, dstWrite, err := s.permForGrid(ctx, tx, userID, req.DestGridID)
		if err != nil {
			return err
		}
		if !dstWrite {
			return ErrPermissionDenied
		}
		// Dest-side locality at the drop point.
		if !req.DestViewRect.Intersects(req.X, req.Y, src.W, src.H) {
			return ErrLocality
		}

		// CoW fork up the destination path.
		dstPre, err := s.preWrite(ctx, tx, userID, req.DestPath, 0)
		if err != nil {
			return err
		}
		events = append(events, dstPre.Events...)
		dstGrid := dstPre.GridID

		// Dest overlap check.
		over, err := overlapsExisting(ctx, tx, dstGrid, req.X, req.Y, src.W, src.H)
		if err != nil {
			return err
		}
		if over {
			return ErrOverlap
		}

		// Copy preview bytes from the current source row.
		previewJPEG, err := loadPreviewJPEG(ctx, tx, src.ID)
		if err != nil {
			return err
		}

		// Parent grid for inheriting owner/group/mode.
		parent, err := s.loadGrid(ctx, tx, dstGrid)
		if err != nil {
			return err
		}
		now := s.now().Unix()
		// Fresh object_id: the fork is a distinct identity, not a clone
		// (clones share object_id; forks do not — they represent a new
		// captured moment).
		newObj := s.newID()
		res, err := tx.ExecContext(ctx, `
			INSERT INTO tiles (object_id, grid_id, type, x, y, w, h, view_x, view_y, view_zoom,
				mime_type, url_string, preview_jpeg, owner_id, group_id, mode, created_at, updated_at)
			VALUES (?, ?, 'file', ?, ?, ?, ?, 0, 0, 0, ?, ?, ?, ?, ?, ?, ?, ?)`,
			newObj, dstGrid, req.X, req.Y, src.W, src.H, rpc.MimeURIList, src.URLString, previewJPEG,
			parent.OwnerID, parent.GroupID, parent.Mode, now, now)
		if err != nil {
			return fmt.Errorf("insert fork: %w", err)
		}
		newID, err := res.LastInsertId()
		if err != nil {
			return err
		}
		out, err = s.loadTile(ctx, tx, newID)
		if err != nil {
			return err
		}
		events = append(events, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *out}})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		s.publish(userID, ev)
	}
	return out, nil
}

// SetURLPreview overwrites a URL tile's preview JPEG. Called by the
// server's URL stream handler once on WS close (the final-frame save).
// No event is published — the WS stream already delivered every frame
// to the descended client, and parent-grid views re-read on next
// GetGrid. Returns ErrNotURLTile if the target isn't a URL tile.
func (s *Store) SetURLPreview(ctx context.Context, _ int64, tileID int64, jpeg []byte) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		t, err := s.loadTile(ctx, tx, tileID)
		if err != nil {
			return err
		}
		if !t.IsURL() {
			return ErrNotURLTile
		}
		_, err = tx.ExecContext(ctx,
			`UPDATE tiles SET preview_jpeg = ?, updated_at = ? WHERE id = ?`,
			jpeg, s.now().Unix(), tileID)
		return err
	})
}

// SetURLString updates a URL tile's stored URL — called by the
// URLDriver when the live tab navigates. Publishes a tile_changed
// event. No permission check (driver-internal).
func (s *Store) SetURLString(ctx context.Context, userID, tileID int64, newURL string) error {
	var out *rpc.Tile
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		t, err := s.loadTile(ctx, tx, tileID)
		if err != nil {
			return err
		}
		if !t.IsURL() {
			return ErrNotURLTile
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET url_string = ?, updated_at = ? WHERE id = ?`,
			newURL, s.now().Unix(), tileID); err != nil {
			return err
		}
		out, err = s.loadTile(ctx, tx, tileID)
		return err
	})
	if err != nil {
		return err
	}
	s.publish(userID, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *out}})
	return nil
}

// suppress unused-import linter when only some symbols are exercised.
var _ = errors.New
