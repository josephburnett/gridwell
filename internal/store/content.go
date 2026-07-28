package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

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
//	url  → the ADDRESS (issue #209: created empty at drop, written at the
//	       first-descent prompt; changing where a tile points bumps)
//
// url/shell FROZEN PREVIEWS ride SetTile (the atomic freeze); wells have no
// local content (a connection well's params are the sshhost plugin's arm).
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

// writeURLContent sets a url tile's address — the url arm of the one
// content write (issue #209). Version-claimed and version-bumping: changing
// where a tile points is a content edit. The address must be a real
// http(s) url — an unconfigured tile is made by CreateURL, never by an
// empty write — and a refused write leaves the old address byte-for-byte
// intact (commit-at-close upstream, one transaction here).
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
		n, err := s.checkTileVersion(ctx, tx, tileID, version)
		if err != nil {
			return err
		}
		if n.Kind != rpc.KindURL {
			return fmt.Errorf("%w: not a url tile", ErrInvalidArgument)
		}
		if n.URLString == urlString {
			// Re-writing the same address is a true no-op (reading and no-op
			// writes never mutate — the primary rule).
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

// ReadContent is the single content-bytes read: the body bytes paired with
// the row version they belong to, read in one call at the owner so a caller
// can never hold a version apart from its bytes (the save-basis contract).
// Media type rides along (blobs are self-describing). A tile with no blob
// yet returns empty bytes and its current version. A url tile's content is
// its address (the WriteContent url arm's mirror, issue #209).
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
