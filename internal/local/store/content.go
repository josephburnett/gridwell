package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// WriteContent is the single content-bytes write: id-addressed,
// version-claimed, one complete value. The RPC layer calls it exactly once, at
// clean close, so a broken stream leaves the old value byte-for-byte intact.
// Version semantics are kind-determined:
//
//	text → a content edit: bumps version, and alt derives from the first line
//	pane → a framing-class layout write: never bumps
//	url  → the address: changing where a tile points bumps
//
// A frozen preview rides SetTile and a well has no local content. A leaf link
// is refused: the row owns no content, and a content operation addresses the
// target the caller names explicitly.
func (s *Store) WriteContent(ctx context.Context, tileID string, version int64, data []byte) (*gridwellv1.Tile, error) {
	t, err := s.GetTile(ctx, tileID)
	if err != nil {
		return nil, err
	}
	if t.LinkTargetId != "" {
		return nil, fmt.Errorf("%w: a link owns no content; write through its target %s",
			ErrInvalidArgument, t.LinkTargetId)
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

// writeURLContent sets a url tile's address. It claims and bumps, because
// changing where a tile points is a content edit. The address must be a real
// http or https url, an unconfigured tile being made by CreateURL, and a
// refused write leaves the old address byte-for-byte intact.
func (s *Store) writeURLContent(ctx context.Context, tileIDStr string, version int64, data []byte) (*gridwellv1.Tile, error) {
	urlString := strings.TrimSpace(string(data))
	if !urlSchemeAllowed(urlString) {
		return nil, fmt.Errorf("%w: only http/https URLs allowed", ErrInvalidArgument)
	}
	tileID, err := parseID(tileIDStr)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	var out *gridwellv1.Tile
	err = s.withMutation(ctx, "WriteContent/url", func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
		n, err := s.claimContentVersion(ctx, tx, tileID, version)
		if err != nil {
			return err
		}
		if n.Kind != rpc.KindURL {
			return fmt.Errorf("%w: not a url tile", ErrInvalidArgument)
		}
		if n.UrlString == urlString {
			// A no-op write never mutates.
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

// ReadContent is the single content-bytes read: the bytes paired with the row
// version they belong to, in one call at the owner, so a caller can never hold
// a version apart from its bytes. A tile with no blob returns empty bytes and
// its current version. A url tile's content is its address.
func (s *Store) ReadContent(ctx context.Context, tileID string) (data []byte, mediaType string, version int64, err error) {
	t, err := s.GetTile(ctx, tileID)
	if err != nil {
		return nil, "", 0, err
	}
	if t.Kind == rpc.KindURL {
		return []byte(t.UrlString), "text/plain; charset=utf-8", t.Version, nil
	}
	if t.BlobId == 0 {
		return nil, "", t.Version, nil
	}
	data, mediaType, err = s.GetBlobWithMedia(ctx, t.BlobId)
	if err != nil {
		return nil, "", 0, err
	}
	return data, mediaType, t.Version, nil
}

// RenameTile is the versioned user rename: it sets alt_text and latches
// alt_user, so every automatic capture defers from then on. setAltTx owns the
// latch arbitration; this verb adds the claim a user edit owes, checked in the
// same transaction as the write. Text tiles are refused, their name being
// derived from the first line of their content.
func (s *Store) RenameTile(ctx context.Context, tileID string, version int64, alt string) (*gridwellv1.Tile, error) {
	id, err := parseID(tileID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	var out *gridwellv1.Tile
	err = s.withMutation(ctx, "RenameTile", func(tx *sql.Tx, events *[]*gridwellv1.Event) error {
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
