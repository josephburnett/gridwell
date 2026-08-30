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
// write truthfully.
//
// staleClaim is the same write with a version the world has moved past. It is
// nil for every mutation whose request carries NO version at all — which is
// most of them since docs/simplify-plan.md S5 reserved those wire fields, and
// is the strongest form of the rule: for those writes a stale claim is not
// ignored, it is unrepresentable, and the compiler is the pin. Where a
// version is still on the signature (WriteContent's kind dispatch), claims
// says whether that arm actually reads it.
type versionCase struct {
	name       string
	subject    func(t *testing.T, s *Store, ctx context.Context, root string) *rpc.Tile
	mutate     func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile) error
	staleClaim func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile) error
	// bumps: the write advances the row's version.
	bumps bool
	// claims: staleClaim is refused with ErrVersionConflict.
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
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile) error {
			_, err := s.WriteContent(ctx, tile.ID, tile.Version, []byte("# edited"))
			return err
		},
		staleClaim: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile) error {
			_, err := s.WriteContent(ctx, tile.ID, tile.Version+7, []byte("# edited"))
			return err
		},
	},
	{
		// Byte-identical bytes are a true no-op: reading and no-op writes
		// never mutate (the primary rule). The claim is still checked.
		name: "WriteContent/text body unchanged", subject: textSubject, bumps: false, claims: true,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile) error {
			_, err := s.WriteContent(ctx, tile.ID, tile.Version, []byte("# hi"))
			return err
		},
		staleClaim: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile) error {
			_, err := s.WriteContent(ctx, tile.ID, tile.Version+7, []byte("# hi"))
			return err
		},
	},
	{
		name: "WriteContent/url address", subject: urlSubject, bumps: true, claims: true,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile) error {
			_, err := s.WriteContent(ctx, tile.ID, tile.Version, []byte("https://elsewhere.example"))
			return err
		},
		staleClaim: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile) error {
			_, err := s.WriteContent(ctx, tile.ID, tile.Version+7, []byte("https://elsewhere.example"))
			return err
		},
	},
	{
		// alt_text IS content when the USER types it (it changes the
		// markdown a drop produces), and the rename latches alt_user.
		name: "RenameTile/user rename", subject: urlSubject, bumps: true, claims: true,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile) error {
			_, err := s.RenameTile(ctx, tile.ID, tile.Version, "a name I typed")
			return err
		},
		staleClaim: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile) error {
			_, err := s.RenameTile(ctx, tile.ID, tile.Version+7, "a name I typed")
			return err
		},
	},

	// ── Captures: what the server observed. No bump, no claim. ─────────
	{
		// The shell detach path baking in the tmux foreground command.
		name: "SetTileAlt/automatic capture", subject: shellSubject, bumps: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile) error {
			return s.SetTileAlt(ctx, mustParseID(t, tile.ID), "vim CLAUDE.md", false)
		},
	},
	{
		name: "SetURLState/freeze capture", subject: urlSubject, bumps: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile) error {
			_, err := s.SetURLState(ctx, &rpc.SetURLStateRequest{
				TileID: tile.ID,
				JPEG:   []byte("jpegbytes"), URL: "https://example.com/deep",
				Title: "Example", History: `["https://example.com"]`,
			})
			return err
		},
	},
	{
		name: "SetShellPreview/frozen frame", subject: shellSubject, bumps: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile) error {
			_, err := s.SetShellPreview(ctx, &rpc.SetShellPreviewRequest{
				TileID: tile.ID, JPEG: []byte("jpegbytes"),
			})
			return err
		},
	},

	// ── Framing: how it looked. No bump, no claim. ─────────────────────
	{
		name: "SetTextView/window and mode", subject: textSubject, bumps: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile) error {
			_, err := s.SetTextView(ctx, &rpc.SetTextViewRequest{
				TileID: tile.ID,
				TextX:  10, TextY: 20, TextW: 300, TextH: 400, TextMode: "rendered",
			})
			return err
		},
	},
	{
		name: "SetContentZoom/content scale", subject: shellSubject, bumps: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile) error {
			_, err := s.SetContentZoom(ctx, &rpc.SetContentZoomRequest{
				TileID: tile.ID, ContentZoom: 1.5,
			})
			return err
		},
	},
	{
		name: "SetURLFrozen/standing freeze", subject: urlSubject, bumps: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile) error {
			_, err := s.SetURLFrozen(ctx, &rpc.SetURLFrozenRequest{
				TileID: tile.ID, Frozen: true,
			})
			return err
		},
	},
	{
		name: "SetFraming/doorway viewport", subject: wellSubject, bumps: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile) error {
			_, err := s.SetFraming(ctx, &rpc.SetFramingRequest{
				TileID:  tile.ID,
				Framing: rpc.Framing{Cx: 3, Cy: 4, Zoom: 1.25},
			})
			return err
		},
	},
	{
		// The one framing write that still SEES a version: a pane layout
		// rides WriteContent's kind dispatch, whose signature carries one
		// for the text and url arms. This arm must ignore it.
		name: "SetPaneLayout/workspace arrangement", subject: paneSubject, bumps: false, claims: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile) error {
			_, err := s.SetPaneLayout(ctx, mustParseID(t, tile.ID), tile.Version,
				[]byte(`{"v":1,"root":{"pane":{"id":"p1","zoom":1}},"focus":"p1"}`))
			return err
		},
		staleClaim: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile) error {
			_, err := s.SetPaneLayout(ctx, mustParseID(t, tile.ID), tile.Version+7,
				[]byte(`{"v":1,"root":{"pane":{"id":"p1","zoom":1}},"focus":"p1"}`))
			return err
		},
	},

	// ── Layout: where it sits. No bump, no claim. ──────────────────────
	{
		name: "PlaceTile/move and resize", subject: textSubject, bumps: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile) error {
			_, err := s.PlaceTile(ctx, &rpc.PlaceTileRequest{
				TileID: tile.ID, GridID: tile.GridID, X: 6, Y: 7, W: 3, H: 3,
			})
			return err
		},
	},
	{
		// The SOURCE row is untouched by a clone; the copy carries its
		// version so the two stay "the same content" until one diverges.
		name: "CloneTile/source row", subject: textSubject, bumps: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile) error {
			_, err := s.CloneTile(ctx, &rpc.CloneTileRequest{
				TileID: tile.ID, DestGridID: tile.GridID, X: 6, Y: 6,
			})
			return err
		},
	},
	{
		// A delete on an ordinary grid MOVES the row into the trash (same
		// id, same row) — layout, so the version is untouched there too.
		name: "DeleteTile/move to trash", subject: textSubject, bumps: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *rpc.Tile) error {
			return s.DeleteTile(ctx, &rpc.DeleteTileRequest{TileID: tile.ID})
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
			if err := c.mutate(t, s, ctx, tile); err != nil {
				t.Fatalf("mutate: %v", err)
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
// carry the user's content claim. Everything else is last-writer-wins — an
// automatic capture, a framing settle, or a drag that raced someone else's
// edit may not be turned into a conflict the user has to notice.
//
// Most cases have no staleClaim at all: their requests carry no version
// field, so the claim is unrepresentable rather than ignored. Those are
// counted here so a case can never quietly grow one back without saying so.
func TestVersionRuleClaim(t *testing.T) {
	claimable := 0
	for _, c := range versionCases {
		if c.staleClaim == nil {
			if c.claims {
				t.Errorf("%s: claims=true but there is no way to send a stale claim", c.name)
			}
			continue
		}
		claimable++
		t.Run(c.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			tile := c.subject(t, s, ctx, rootID(t, s))
			err := c.staleClaim(t, s, ctx, tile)
			if c.claims && !errors.Is(err, ErrVersionConflict) {
				t.Errorf("stale claim: got %v, want ErrVersionConflict", err)
			}
			if !c.claims && errors.Is(err, ErrVersionConflict) {
				t.Errorf("stale claim was refused, but this write carries no claim: %v", err)
			}
		})
	}
	// The four content arms plus the pane arm that must IGNORE its version.
	// A change to this number is a change to the rule: say so in the commit.
	if claimable != 5 {
		t.Errorf("%d writes can be handed a version, want 5 — a new one appeared, or one lost its claim", claimable)
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
		TileID: well.ID, ContentZoom: 2,
	}); err == nil {
		t.Error("SetContentZoom on a well must be refused")
	}
}
