package store

import (
	"context"
	"errors"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

// This file is the ONE pin on what a tile row's `version` MEANS
// (docs/simplify-plan.md S5, owner decision 2026-08-29):
//
//	version = "the USER'S CONTENT BYTES changed".
//
// It is the optimistic-concurrency claim for those edits and nothing else.
// Everything else the store writes to a tile row rides the same tile event
// and is LAST-WRITER-WINS, with no claim to lose and no bump to make:
//
//   - CAPTURES — a page title, a preview jpeg, a url history, a shell's
//     foreground command, a frozen face. Facts the server OBSERVED, not
//     edits the user made; a capture must never outrank (or be outranked
//     by) the user's own claim.
//   - FRAMING — where the viewport sat, a text tile's window and mode, the
//     content zoom, a standing url freeze, a pane tile's layout.
//   - LAYOUT — place / move / resize / clone / delete. An explicit user act
//     on a tile the user can SEE; when two clients race, the physical-world
//     answer is "whoever moved it last moved it", reconciled by the event.
//     Overlap (the one thing a race could actually corrupt) is refused
//     server-side inside the same transaction, claim or no claim.
//
// The table below is the whole rule. A new mutation adds a row here; a row
// that cannot be written is a mutation that broke the rule.

// tileVersion reads a tile's current version straight from the row.
func tileVersion(t *testing.T, s *Store, tileID string) int64 {
	t.Helper()
	id, err := parseID(tileID)
	if err != nil {
		t.Fatal(err)
	}
	var v int64
	if err := s.db.QueryRow(`SELECT version FROM tiles WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatalf("read version: %v", err)
	}
	return v
}

// versionCase is one store mutation and what the version rule says about it.
// subject builds the fixture row the mutation runs against; mutate runs the
// write with the claim it is handed (mutations that carry no claim ignore
// it, which is exactly what the claims=false subtest proves).
type versionCase struct {
	name    string
	subject func(t *testing.T, s *Store, ctx context.Context, root string) *rpc.Tile
	mutate  func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile, claim int64) error
	// bumps: a truthful claim advances the row's version.
	bumps bool
	// claims: a STALE claim is refused with ErrVersionConflict.
	claims bool
}

func textSubject(t *testing.T, s *Store, ctx context.Context, root string) *rpc.Tile {
	t.Helper()
	tile, err := s.CreateText(ctx, &rpc.CreateTextRequest{GridID: root, X: 0, Y: 0, W: 2, H: 2, Data: []byte("# hi")})
	if err != nil {
		t.Fatal(err)
	}
	return tile
}

func urlSubject(t *testing.T, s *Store, ctx context.Context, root string) *rpc.Tile {
	t.Helper()
	tile, err := s.CreateURL(ctx, &rpc.CreateURLRequest{GridID: root, X: 0, Y: 0, W: 2, H: 2, URL: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	return tile
}

func shellSubject(t *testing.T, s *Store, ctx context.Context, root string) *rpc.Tile {
	t.Helper()
	tile, err := s.CreateShell(ctx, &rpc.CreateShellRequest{GridID: root, X: 0, Y: 0, W: 2, H: 2})
	if err != nil {
		t.Fatal(err)
	}
	return tile
}

func wellSubject(t *testing.T, s *Store, ctx context.Context, root string) *rpc.Tile {
	t.Helper()
	tile, err := s.CreateWell(ctx, &rpc.CreateWellRequest{GridID: root, X: 0, Y: 0, W: 2, H: 2})
	if err != nil {
		t.Fatal(err)
	}
	return tile
}

func paneSubject(t *testing.T, s *Store, ctx context.Context, root string) *rpc.Tile {
	t.Helper()
	tile, err := s.CreatePane(ctx, root, 0, 0, 2, 2, "ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	return tile
}

var versionCases = []versionCase{
	// ── Content: the user's own bytes. Bumps, and claims. ──────────────
	{
		name: "WriteContent/text body", subject: textSubject, bumps: true, claims: true,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile, claim int64) error {
			_, err := s.WriteContent(ctx, tile.ID, claim, []byte("# edited"))
			return err
		},
	},
	{
		// Byte-identical bytes are a true no-op: reading and no-op writes
		// never mutate (the primary rule).
		name: "WriteContent/text body unchanged", subject: textSubject, bumps: false, claims: true,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile, claim int64) error {
			_, err := s.WriteContent(ctx, tile.ID, claim, []byte("# hi"))
			return err
		},
	},
	{
		name: "WriteContent/url address", subject: urlSubject, bumps: true, claims: true,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile, claim int64) error {
			_, err := s.WriteContent(ctx, tile.ID, claim, []byte("https://elsewhere.example"))
			return err
		},
	},
	{
		// alt_text IS content when the USER types it (it changes the
		// markdown a drop produces), and the rename latches alt_user.
		name: "RenameTile/user rename", subject: urlSubject, bumps: true, claims: true,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile, claim int64) error {
			_, err := s.RenameTile(ctx, tile.ID, claim, "a name I typed")
			return err
		},
	},

	// ── Captures: what the server observed. No bump, no claim. ─────────
	{
		// The shell detach path baking in the tmux foreground command.
		name: "SetTileAlt/automatic capture", subject: shellSubject, bumps: false, claims: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile, claim int64) error {
			return s.SetTileAlt(ctx, mustParseID(t, tile.ID), "vim CLAUDE.md", false)
		},
	},
	{
		name: "SetURLState/freeze capture", subject: urlSubject, bumps: false, claims: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile, claim int64) error {
			_, err := s.SetURLState(ctx, &rpc.SetURLStateRequest{
				TileID: tile.ID, Version: claim,
				JPEG: []byte("jpegbytes"), URL: "https://example.com/deep",
				Title: "Example", History: `["https://example.com"]`,
			})
			return err
		},
	},
	{
		name: "SetShellPreview/frozen frame", subject: shellSubject, bumps: false, claims: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile, claim int64) error {
			_, err := s.SetShellPreview(ctx, &rpc.SetShellPreviewRequest{
				TileID: tile.ID, Version: claim, JPEG: []byte("jpegbytes"),
			})
			return err
		},
	},

	// ── Framing: how it looked. No bump, no claim. ─────────────────────
	{
		name: "SetTextView/window and mode", subject: textSubject, bumps: false, claims: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile, claim int64) error {
			_, err := s.SetTextView(ctx, &rpc.SetTextViewRequest{
				TileID: tile.ID, Version: claim,
				TextX: 10, TextY: 20, TextW: 300, TextH: 400, TextMode: "rendered",
			})
			return err
		},
	},
	{
		name: "SetContentZoom/content scale", subject: shellSubject, bumps: false, claims: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile, claim int64) error {
			_, err := s.SetContentZoom(ctx, &rpc.SetContentZoomRequest{
				TileID: tile.ID, Version: claim, ContentZoom: 1.5,
			})
			return err
		},
	},
	{
		name: "SetURLFrozen/standing freeze", subject: urlSubject, bumps: false, claims: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile, claim int64) error {
			_, err := s.SetURLFrozen(ctx, &rpc.SetURLFrozenRequest{
				TileID: tile.ID, Version: claim, Frozen: true,
			})
			return err
		},
	},
	{
		name: "SetFraming/doorway viewport", subject: wellSubject, bumps: false, claims: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile, claim int64) error {
			_, err := s.SetFraming(ctx, &rpc.SetFramingRequest{
				TileID: tile.ID, Version: claim,
				Framing: rpc.Framing{Cx: 3, Cy: 4, Zoom: 1.25},
			})
			return err
		},
	},
	{
		name: "SetPaneLayout/workspace arrangement", subject: paneSubject, bumps: false, claims: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile, claim int64) error {
			_, err := s.SetPaneLayout(ctx, mustParseID(t, tile.ID), claim,
				[]byte(`{"v":1,"root":{"pane":{"id":"p1","zoom":1}},"focus":"p1"}`))
			return err
		},
	},

	// ── Layout: where it sits. No bump, no claim. ──────────────────────
	{
		name: "PlaceTile/move and resize", subject: textSubject, bumps: false, claims: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile, claim int64) error {
			_, err := s.PlaceTile(ctx, &rpc.PlaceTileRequest{
				TileID: tile.ID, Version: claim, GridID: tile.GridID, X: 6, Y: 7, W: 3, H: 3,
			})
			return err
		},
	},
	{
		// The SOURCE row is untouched by a clone; the copy carries its
		// version so the two stay "the same content" until one diverges.
		name: "CloneTile/source row", subject: textSubject, bumps: false, claims: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile, claim int64) error {
			_, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
				TileID: tile.ID, Version: claim, DestGridID: tile.GridID, X: 6, Y: 6,
			})
			return err
		},
	},
	{
		// A delete on an ordinary grid MOVES the row into the trash (same
		// id, same row) — layout, so the version is untouched there too.
		name: "DeleteTile/move to trash", subject: textSubject, bumps: false, claims: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile, claim int64) error {
			return s.DeleteTile(ctx, &rpc.DeleteTileRequest{TileID: tile.ID, Version: claim})
		},
	},
}

// TestVersionRuleBump: a truthful write advances the row version iff it
// changed the user's content bytes.
func TestVersionRuleBump(t *testing.T) {
	for _, c := range versionCases {
		t.Run(c.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			tile := c.subject(t, s, ctx, rootID(t, s))
			v0 := tile.Version
			if err := c.mutate(t, s, ctx, tile, v0); err != nil {
				t.Fatalf("mutate with a truthful claim: %v", err)
			}
			got := tileVersion(t, s, tile.ID)
			want := v0
			if c.bumps {
				want = v0 + 1
			}
			if got != want {
				t.Errorf("version %d -> %d, want %d (bumps=%v)", v0, got, want, c.bumps)
			}
		})
	}
}

// TestVersionRuleClaim: a STALE version is refused only by the writes that
// carry the user's content claim. Everything else is last-writer-wins and
// must accept it — an automatic capture, a framing settle, or a drag that
// raced someone else's edit may not be turned into a conflict the user has
// to notice.
func TestVersionRuleClaim(t *testing.T) {
	for _, c := range versionCases {
		t.Run(c.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			tile := c.subject(t, s, ctx, rootID(t, s))
			err := c.mutate(t, s, ctx, tile, tile.Version+7)
			if c.claims && !errors.Is(err, ErrVersionConflict) {
				t.Errorf("stale claim: got %v, want ErrVersionConflict", err)
			}
			if !c.claims && errors.Is(err, ErrVersionConflict) {
				t.Errorf("stale claim was refused, but this write carries no claim: %v", err)
			}
		})
	}
}

// TestContentZoomRefusesWells (issue #82): a well's view_zoom is the grid
// viewport, a different fact with its own writer — SetContentZoom refuses it.
// (The version half of this rule lives in the table above.)
func TestContentZoomRefusesWells(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)

	well, err := s.CreateWell(ctx, &rpc.CreateWellRequest{GridID: root, X: 3, Y: 3, W: 1, H: 1})
	if err != nil {
		t.Fatalf("CreateWell: %v", err)
	}
	if _, err := s.SetContentZoom(ctx, &rpc.SetContentZoomRequest{
		TileID: well.ID, Version: well.Version, ContentZoom: 2,
	}); err == nil {
		t.Error("SetContentZoom on a well must be refused")
	}
}
