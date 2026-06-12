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
// server's URL stream handler on WS close. The JPEG is hash-deduped
// through the blobs table — two tiles whose previews happen to be
// identical bytes share one row. Bumps the tile's version.
func (s *Store) SetURLPreview(ctx context.Context, tileID int64, jpeg []byte) error {
	return s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		t, err := s.loadTile(ctx, tx, tileID)
		if err != nil {
			return err
		}
		if t.Kind != rpc.KindURL {
			return ErrNotURLTile
		}
		oldBlobID := t.PreviewBlobID
		var newBlobID int64
		if len(jpeg) > 0 {
			hash := hashBytes(jpeg)
			newBlobID, err = putBlob(ctx, tx, hash, jpeg)
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
			newArg, s.now().Unix(), tileID); err != nil {
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

// SetURLState freezes a live URL tile in one mutation: it writes the
// preview JPEG (with blob refcounting), the address, and the page title,
// then bumps the version once and publishes a single tile_changed event.
// Empty arguments are skipped so a partial capture (e.g. a failed final
// frame) never clobbers good state. This is the RPC the Electron shell
// calls on ascend, replacing the old server-side rod closeSession freeze.
func (s *Store) SetURLState(ctx context.Context, tileID int64, jpeg []byte, url, title string) (*rpc.Tile, error) {
	var out *rpc.Tile
	err := s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		t, err := s.loadTile(ctx, tx, tileID)
		if err != nil {
			return err
		}
		if t.Kind != rpc.KindURL {
			return ErrNotURLTile
		}

		if len(jpeg) > 0 {
			oldBlobID := t.PreviewBlobID
			hash := hashBytes(jpeg)
			newBlobID, err := putBlob(ctx, tx, hash, jpeg)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE tiles SET preview_blob_id = ?, updated_at = ? WHERE id = ?`,
				newBlobID, s.now().Unix(), tileID); err != nil {
				return err
			}
			if oldBlobID != newBlobID {
				if err := s.incBlobRefcount(ctx, tx, newBlobID); err != nil {
					return err
				}
				if oldBlobID != 0 {
					if err := s.decBlobRefcount(ctx, tx, oldBlobID); err != nil {
						return err
					}
				}
			}
		}
		if url != "" {
			if _, err := tx.ExecContext(ctx,
				`UPDATE tiles SET url_string = ?, updated_at = ? WHERE id = ?`,
				url, s.now().Unix(), tileID); err != nil {
				return err
			}
		}
		if title != "" {
			if _, err := tx.ExecContext(ctx,
				`UPDATE tiles SET alt_text = ?, updated_at = ? WHERE id = ?`,
				title, s.now().Unix(), tileID); err != nil {
				return err
			}
		}

		if err := bumpTileVersion(ctx, tx, tileID); err != nil {
			return err
		}
		out, err = s.loadTile(ctx, tx, tileID)
		if err != nil {
			return err
		}
		*events = append(*events, rpc.Event{Kind: rpc.EventTileChanged, TileChanged: &rpc.TileChanged{Tile: *out}})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
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
			alt, s.now().Unix(), tileID); err != nil {
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
