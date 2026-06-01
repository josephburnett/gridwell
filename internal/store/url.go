package store

import (
	"context"
	"database/sql"
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

// SetURLPreview overwrites a URL tile's preview JPEG. Called by the
// server's URL stream handler on WS close. Bumps the tile's version.
func (s *Store) SetURLPreview(ctx context.Context, tileID int64, jpeg []byte) error {
	return s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		t, err := s.loadTile(ctx, tx, tileID)
		if err != nil {
			return err
		}
		if t.Kind != rpc.KindURL {
			return ErrNotURLTile
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET preview_jpeg = ?, updated_at = ? WHERE id = ?`,
			jpeg, s.now().Unix(), tileID); err != nil {
			return err
		}
		if err := bumpTileVersion(ctx, tx, tileID); err != nil {
			return err
		}
		out, err := s.loadTile(ctx, tx, tileID)
		if err != nil {
			return err
		}
		*events = append(*events, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *out}})
		return nil
	})
}

// SetTileAlt updates a tile's stored alt-text. Used by the URL stream
// handler at session close to bake the page title into the tile so
// embed drops can carry a meaningful label. Bumps the tile's version.
func (s *Store) SetTileAlt(ctx context.Context, tileID int64, alt string) error {
	return s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		if _, err := s.loadTile(ctx, tx, tileID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET alt_text = ?, updated_at = ? WHERE id = ?`,
			sql.NullString{String: alt, Valid: alt != ""}, s.now().Unix(), tileID); err != nil {
			return err
		}
		if err := bumpTileVersion(ctx, tx, tileID); err != nil {
			return err
		}
		out, err := s.loadTile(ctx, tx, tileID)
		if err != nil {
			return err
		}
		*events = append(*events, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *out}})
		return nil
	})
}

// SetURLString updates a URL tile's stored URL — called by the URLDriver
// when the live tab navigates. Bumps the tile's version and publishes a
// tile_changed event.
func (s *Store) SetURLString(ctx context.Context, tileID int64, newURL string) error {
	return s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		t, err := s.loadTile(ctx, tx, tileID)
		if err != nil {
			return err
		}
		if t.Kind != rpc.KindURL {
			return ErrNotURLTile
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET url_string = ?, updated_at = ? WHERE id = ?`,
			newURL, s.now().Unix(), tileID); err != nil {
			return err
		}
		if err := bumpTileVersion(ctx, tx, tileID); err != nil {
			return err
		}
		out, err := s.loadTile(ctx, tx, tileID)
		if err != nil {
			return err
		}
		*events = append(*events, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *out}})
		return nil
	})
}
