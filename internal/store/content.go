package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// WriteContent is the single content-bytes write (2026-07-26,
// interface-redesign-plan.md decision 5): id-addressed, version-claimed, one
// complete value. The gRPC layer assembles the client stream and calls this
// exactly once, at clean close — commit-at-close means nothing here runs for
// a broken stream, so the old value stays byte-for-byte intact.
//
// Version semantics are kind-determined in the store's one table, extended:
//
//	text → content edit (bumps version; alt derives from the first line)
//	pane → framing-class layout write (never bumps; owner decision 2026-07-08)
//
// url/shell content is the frozen preview and rides SetTile (the atomic
// freeze); wells have no content (yet — creation params are future-reserved).
// A leaf LINK is refused: the row owns no content, and content ops address
// the target the caller names explicitly (reads resolve at the serving node).
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

// ReadContent is the single content-bytes read: the body bytes paired with
// the row version they belong to, read in one call at the owner so a caller
// can never hold a version apart from its bytes (the save-basis contract).
// Media type rides along (blobs are self-describing). A tile with no blob
// yet returns empty bytes and its current version.
func (s *Store) ReadContent(ctx context.Context, tileID string) (data []byte, mediaType string, version int64, err error) {
	t, err := s.GetTile(ctx, tileID)
	if err != nil {
		return nil, "", 0, err
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

// RenameTile is the versioned USER rename (2026-07-26 decision 6, folding the
// old unversioned wire SetTileAlt into SetTile): sets alt_text and latches
// alt_user so every automatic capture (url page title, shell foreground
// command) defers from then on. The latch arbitration stays in the one place
// it always was (setAltTx, shared with SetTileAlt); this verb adds the
// optimistic-concurrency claim a user edit owes, checked in the same
// transaction as the write. Text tiles are refused — their name derives from
// the first line of their content.
func (s *Store) RenameTile(ctx context.Context, tileID string, version int64, alt string) (*rpc.Tile, error) {
	id, err := parseID(tileID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid tile_id", ErrInvalidArgument)
	}
	var out *rpc.Tile
	err = s.withMutation(ctx, func(tx *sql.Tx, events *[]rpc.Event) error {
		n, err := s.checkTileVersion(ctx, tx, id, version)
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
