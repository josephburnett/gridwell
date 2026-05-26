package store

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// URLDriver is the abstraction the store uses for URL-tile presence.
type URLDriver interface {
	Available() bool
}

// FakeURLDriver is an in-memory URLDriver for tests.
type FakeURLDriver struct {
	mu        sync.Mutex
	available bool
}

func NewFakeURLDriver() *FakeURLDriver {
	return &FakeURLDriver{available: true}
}

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
// preview JPEG.
func (s *Store) ForkURL(ctx context.Context, req *rpc.ForkURLRequest) (*rpc.Tile, error) {
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
		if !req.ViewRect.Intersects(src.X, src.Y, src.W, src.H) {
			return ErrLocality
		}
		if _, err := s.buildGridSequence(ctx, tx, req.Path); err != nil {
			return err
		}

		dstSeq, err := s.buildGridSequence(ctx, tx, req.DestPath)
		if err != nil {
			return err
		}
		if dstSeq.grids[len(dstSeq.grids)-1] != req.DestGridID {
			return fmt.Errorf("%w: dest path leaf mismatch", ErrInvalidPath)
		}
		if !req.DestViewRect.Intersects(req.X, req.Y, src.W, src.H) {
			return ErrLocality
		}

		dstPre, err := s.preWrite(ctx, tx, req.DestPath, 0)
		if err != nil {
			return err
		}
		events = append(events, dstPre.Events...)
		dstGrid := dstPre.GridID

		over, err := overlapsExisting(ctx, tx, dstGrid, req.X, req.Y, src.W, src.H)
		if err != nil {
			return err
		}
		if over {
			return ErrOverlap
		}

		previewJPEG, err := loadPreviewJPEG(ctx, tx, src.ID)
		if err != nil {
			return err
		}

		now := s.now().Unix()
		// Fresh object_id: fork is a distinct identity, not a clone.
		newObj := s.newID()
		res, err := tx.ExecContext(ctx, `
			INSERT INTO tiles (object_id, grid_id, type, x, y, w, h, view_x, view_y, view_zoom,
				mime_type, url_string, preview_jpeg, created_at, updated_at)
			VALUES (?, ?, 'file', ?, ?, ?, ?, 0, 0, 0, ?, ?, ?, ?, ?)`,
			newObj, dstGrid, req.X, req.Y, src.W, src.H, rpc.MimeURIList, src.URLString, previewJPEG, now, now)
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
		s.publish(ev)
	}
	return out, nil
}

// SetURLPreview overwrites a URL tile's preview JPEG. Called by the
// server's URL stream handler on WS close.
func (s *Store) SetURLPreview(ctx context.Context, tileID int64, jpeg []byte) error {
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

// SetURLString updates a URL tile's stored URL — called by the URLDriver
// when the live tab navigates. Publishes a tile_changed event.
func (s *Store) SetURLString(ctx context.Context, tileID int64, newURL string) error {
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
	s.publish(rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *out}})
	return nil
}
