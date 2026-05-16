package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// URLDriver is the abstraction the store uses to manage live URL tiles.
// The production implementation drives headless Chromium via CDP; tests
// use a FakeURLDriver that tracks liveness in memory.
//
// All methods are safe for concurrent use.
type URLDriver interface {
	// Available reports whether the driver is functional (e.g. the
	// Chromium binary was found at startup). When false, all live-tile
	// operations should return ErrChromiumUnavailable.
	Available() bool

	// Wake spawns a live tab for (userID, tileID) at initialURL, or
	// no-ops if one already exists. The driver is responsible for
	// streaming preview frames back into the store via SetTilePreview
	// and propagating in-page navigations via SetURLString.
	Wake(ctx context.Context, userID, tileID int64, initialURL string) error

	// Capture closes the live tab for (userID, tileID), ensuring the
	// last frame is persisted to the tile's preview_jpeg before
	// returning. No-ops if the tile is already dormant.
	Capture(ctx context.Context, userID, tileID int64) error

	// IsLive reports whether (userID, tileID) currently has a live tab.
	IsLive(userID, tileID int64) bool
}

// FakeURLDriver is an in-memory URLDriver. It tracks liveness without
// actually running anything. The driver's Available flag is settable so
// tests can simulate a Chromium-absent environment.
type FakeURLDriver struct {
	mu        sync.Mutex
	live      map[liveKey]string // (user, tile) -> last URL
	available bool
}

type liveKey struct {
	user, tile int64
}

// NewFakeURLDriver returns a FakeURLDriver with Available = true.
func NewFakeURLDriver() *FakeURLDriver {
	return &FakeURLDriver{
		live:      map[liveKey]string{},
		available: true,
	}
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

func (d *FakeURLDriver) Wake(ctx context.Context, userID, tileID int64, initialURL string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.available {
		return ErrChromiumUnavailable
	}
	d.live[liveKey{userID, tileID}] = initialURL
	return nil
}

func (d *FakeURLDriver) Capture(ctx context.Context, userID, tileID int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.available {
		return ErrChromiumUnavailable
	}
	delete(d.live, liveKey{userID, tileID})
	return nil
}

func (d *FakeURLDriver) IsLive(userID, tileID int64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.live[liveKey{userID, tileID}]
	return ok
}

// WakeURL transitions a URL tile from dormant to live. Idempotent: if
// the tile is already live, Wake is still called on the driver (which
// no-ops); the response reflects the current state. Requires write
// permission on the tile and locality (tile footprint intersects
// req.ViewRect).
func (s *Store) WakeURL(ctx context.Context, userID int64, req *rpc.WakeURLRequest) (*rpc.Tile, error) {
	if s.urlDriver == nil || !s.urlDriver.Available() {
		return nil, ErrChromiumUnavailable
	}
	tile, err := s.loadTileForURLOp(ctx, userID, req.TileID, req.ViewRect, req.Path)
	if err != nil {
		return nil, err
	}
	if err := s.urlDriver.Wake(ctx, userID, tile.ID, tile.URLString); err != nil {
		return nil, err
	}
	// Reflect liveness in the response and in the broadcast event.
	tile.Live = s.urlDriver.IsLive(userID, tile.ID)
	s.publish(userID, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *tile}})
	return tile, nil
}

// CaptureURL transitions a URL tile from live to dormant. Idempotent: if
// already dormant, the driver no-ops. Requires write permission and
// locality.
func (s *Store) CaptureURL(ctx context.Context, userID int64, req *rpc.CaptureURLRequest) (*rpc.Tile, error) {
	if s.urlDriver == nil {
		return nil, ErrChromiumUnavailable
	}
	tile, err := s.loadTileForURLOp(ctx, userID, req.TileID, req.ViewRect, req.Path)
	if err != nil {
		return nil, err
	}
	// Capture closes the tab and (in the real driver) flushes the last
	// frame to preview_jpeg before returning. We don't require Available
	// here: even a non-Available driver should let us mark a tile
	// dormant (which is a no-op locally).
	if err := s.urlDriver.Capture(ctx, userID, tile.ID); err != nil {
		return nil, err
	}
	// Reload after capture in case the driver updated preview_jpeg.
	tile, err = s.loadTile(ctx, s.db, tile.ID)
	if err != nil {
		return nil, err
	}
	tile.Live = s.urlDriver.IsLive(userID, tile.ID)
	s.publish(userID, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *tile}})
	return tile, nil
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

// SetURLPreview overwrites a URL tile's preview JPEG and publishes a
// url_preview_updated event. Called by the URLDriver as new frames
// arrive (no permission check — the driver is trusted). Returns
// ErrNotURLTile if the target isn't a URL tile.
func (s *Store) SetURLPreview(ctx context.Context, userID, tileID int64, jpeg []byte) error {
	err := s.withTx(ctx, func(tx *sql.Tx) error {
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
	if err != nil {
		return err
	}
	// Look up grid id outside the tx for the event payload.
	var gridID int64
	if err := s.db.QueryRowContext(ctx, `SELECT grid_id FROM tiles WHERE id = ?`, tileID).Scan(&gridID); err != nil {
		return err
	}
	s.publish(userID, rpc.Event{
		Kind:              rpc.EventURLPreviewUpdated,
		URLPreviewUpdated: &rpc.URLPreviewUpdated{GridID: gridID, TileID: tileID},
	})
	return nil
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

// loadTileForURLOp validates a URL-tile mutation up front: tile exists,
// is a URL tile, intersects the view rect, and the user has write
// permission. It does not perform a CoW fork because URL operations
// (Wake/Capture) toggle runtime state only and never modify the
// persistent tile fields.
func (s *Store) loadTileForURLOp(ctx context.Context, userID, tileID int64, vr rpc.ViewRect, _ rpc.Path) (*rpc.Tile, error) {
	tile, err := s.loadTile(ctx, s.db, tileID)
	if err != nil {
		return nil, err
	}
	if !tile.IsURL() {
		return nil, ErrNotURLTile
	}
	if !vr.Intersects(tile.X, tile.Y, tile.W, tile.H) {
		return nil, ErrLocality
	}
	_, write, err := s.permForTile(ctx, s.db, userID, tileID)
	if err != nil {
		return nil, err
	}
	if !write {
		return nil, ErrPermissionDenied
	}
	return tile, nil
}

// suppress unused-import linter when only some symbols are exercised.
var _ = errors.New
