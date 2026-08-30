package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/josephburnett/gridwell/api/rpc"
)

// WriteContent is the single content-bytes write: id-addressed,
// version-claimed, one complete value. The RPC layer assembles the client
// stream and calls this exactly once, at clean close, so nothing here runs for
// a broken stream and the old value stays byte-for-byte intact.
//
// Version semantics are kind-determined in the store's one table:
//
//	text → a content edit: bumps version, and alt derives from the first line
//	pane → a framing-class layout write: never bumps
//	url  → the address: changing where a tile points bumps
//
// A url or shell tile's frozen preview rides SetTile, the atomic freeze, and
// a well has no local content. A leaf link is refused: the row owns no
// content, and content operations address the target the caller names
// explicitly, with reads resolving at the serving node.
func (s *Store) WriteContent(ctx context.Context, tileID string, version int64, data []byte) (*rpc.Tile, error) {
	t, err := s.GetTile(ctx, tileID)
	if err != nil {
		return nil, err
	}
	if t.LinkTargetID != "" {
		return nil, fmt.Errorf("%w: a link owns no content; write through its target %s",
			ErrInvalidArgument, t.LinkTargetID)
	}
	switch t.Kind {
	case rpc.KindText:
		return s.writeTextContent(ctx, tileID, version, data)
	case rpc.KindURL:
		return s.writeURLContent(ctx, tileID, version, data)
	case rpc.KindPane:
		id, err := parseID(tileID)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
		}
		return s.SetPaneLayout(ctx, id, version, data)
	default:
		return nil, fmt.Errorf("%w: a %s tile has no writable content", ErrInvalidArgument, t.Kind)
	}
}

// writeURLContent sets a url tile's address: the url arm of the one content
// write. It is version-claimed and version-bumping, because changing where a
// tile points is a content edit. The address must be a real http or https url
// — an unconfigured tile is made by CreateURL, never by an empty write — and a
// refused write leaves the old address byte-for-byte intact.
func (s *Store) writeURLContent(ctx context.Context, tileIDStr string, version int64, data []byte) (*rpc.Tile, error) {
	urlString := strings.TrimSpace(string(data))
	if !urlSchemeAllowed(urlString) {
		return nil, fmt.Errorf("%w: only http/https URLs allowed", ErrInvalidArgument)
	}
	tileID, err := parseID(tileIDStr)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	var out *rpc.Tile
	err = s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		n, err := s.claimContentVersion(ctx, tx, tileID, version)
		if err != nil {
			return err
		}
		if n.Kind != rpc.KindURL {
			return fmt.Errorf("%w: not a url tile", ErrInvalidArgument)
		}
		if n.URLString == urlString {
			// Re-writing the same address is a true no-op: a no-op write
			// never mutates.
			out = n
			return nil
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tiles SET url_string = ?, updated_at = ? WHERE id = ?`,
			urlString, s.now().Unix(), tileID); err != nil {
			return err
		}
		out, err = s.finishContentEdit(ctx, tx, tileID, events)
		return err
	})
	return out, err
}

// ReadContent is the single content-bytes read: the body bytes paired with the
// row version they belong to, read in one call at the owner so a caller can
// never hold a version apart from its bytes. The media type rides along,
// because blobs are self-describing. A tile with no blob yet returns empty
// bytes and its current version. A url tile's content is its address, the
// mirror of WriteContent's url arm.
func (s *Store) ReadContent(ctx context.Context, tileID string) (data []byte, mediaType string, version int64, err error) {
	t, err := s.GetTile(ctx, tileID)
	if err != nil {
		return nil, "", 0, err
	}
	if t.Kind == rpc.KindURL {
		return []byte(t.URLString), "text/plain; charset=utf-8", t.Version, nil
	}
	if t.BlobID == 0 {
		return nil, "", t.Version, nil
	}
	data, mediaType, err = s.GetBlobWithMedia(ctx, t.BlobID)
	if err != nil {
		return nil, "", 0, err
	}
	return data, mediaType, t.Version, nil
}

// RenameTile is the versioned user rename: it sets alt_text and latches
// alt_user so every automatic capture — a url page title, a shell foreground
// command — defers from then on. The latch arbitration lives in setAltTx,
// shared with SetTileAlt; this verb adds the optimistic-concurrency claim a
// user edit owes, checked in the same transaction as the write. Text tiles are
// refused: their name derives from the first line of their content.
func (s *Store) RenameTile(ctx context.Context, tileID string, version int64, alt string) (*rpc.Tile, error) {
	id, err := parseID(tileID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	var out *rpc.Tile
	err = s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		n, err := s.claimContentVersion(ctx, tx, id, version)
		if err != nil {
			return err
		}
		if n.Kind == rpc.KindText {
			return fmt.Errorf("%w: a text tile's name derives from its first line; rename the content instead", ErrInvalidArgument)
		}
		if err := s.setAltTx(ctx, tx, id, alt, true, events); err != nil {
			return err
		}
		out, err = s.loadTile(ctx, tx, id)
		return err
	})
	return out, err
}
