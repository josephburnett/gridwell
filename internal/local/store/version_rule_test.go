package store

import (
	"context"
	"errors"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

func zoomOf(z float64) rpc.ContentZoom {
	c, err := rpc.NewContentZoom(z)
	if err != nil {
		panic(err)
	}
	return c
}

// The one pin on what a tile row's `version` means: the user's content bytes
// changed, a pane tile's layout among them. It is the optimistic-concurrency
// claim for those edits. Everything else the store writes to a tile row rides
// the same tile event and bumps nothing:
//
//   - captures, facts the server observed rather than edits the user made, so
//     a capture must never outrank the user's own claim;
//   - framing, the viewport and the text window, last-writer-wins;
//   - layout: place, move, resize, clone, delete. A move claims the placement
//     it saw rather than a version, so it never conflicts with typing; overlap
//     is refused server-side in the same transaction.
//
// The table below is the whole rule. A new mutation adds a row here; a row
// that cannot be written is a mutation that broke the rule.

func placementOf(t *gridwellv1.Tile) *gridwellv1.Placement {
	return &gridwellv1.Placement{GridId: t.GridId, X: t.X, Y: t.Y, W: t.W, H: t.H}
}

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
// staleClaim is the same write with a version the world has moved past, and is
// nil for every mutation whose request carries no version at all: for those a
// stale claim is unrepresentable rather than ignored, and the compiler is the
// pin. Where a version is still on the signature, claims says whether that arm
// reads it.
type versionCase struct {
	name       string
	subject    func(t *testing.T, s *Store, ctx context.Context, root string) *gridwellv1.Tile
	mutate     func(t *testing.T, s *Store, ctx context.Context, tile *gridwellv1.Tile) error
	staleClaim func(t *testing.T, s *Store, ctx context.Context, tile *gridwellv1.Tile) error
	// bumps: the write advances the row's version.
	bumps bool
	// claims: staleClaim is refused with ErrVersionConflict.
	claims bool
}

func textSubject(t *testing.T, s *Store, ctx context.Context, root string) *gridwellv1.Tile {
	t.Helper()
	tile, err := s.CreateText(ctx, root, 0, 0, 2, 2, []byte("# hi"))
	if err != nil {
		t.Fatal(err)
	}
	return tile
}

func urlSubject(t *testing.T, s *Store, ctx context.Context, root string) *gridwellv1.Tile {
	t.Helper()
	tile, err := s.CreateURL(ctx, root, 0, 0, 2, 2, "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	return tile
}

func shellSubject(t *testing.T, s *Store, ctx context.Context, root string) *gridwellv1.Tile {
	t.Helper()
	tile, err := s.CreateShell(ctx, root, 0, 0, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	return tile
}

func wellSubject(t *testing.T, s *Store, ctx context.Context, root string) *gridwellv1.Tile {
	t.Helper()
	tile, err := s.CreateWell(ctx, root, 0, 0, 2, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	return tile
}

func paneSubject(t *testing.T, s *Store, ctx context.Context, root string) *gridwellv1.Tile {
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
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *gridwellv1.Tile) error {
			_, err := s.WriteContent(ctx, tile.Id, tile.Version, []byte("# edited"))
			return err
		},
		staleClaim: func(t *testing.T, s *Store, ctx context.Context, tile *gridwellv1.Tile) error {
			_, err := s.WriteContent(ctx, tile.Id, tile.Version+7, []byte("# edited"))
			return err
		},
	},
	{
		// A no-op write never mutates, and the claim is still checked.
		name: "WriteContent/text body unchanged", subject: textSubject, bumps: false, claims: true,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *gridwellv1.Tile) error {
			_, err := s.WriteContent(ctx, tile.Id, tile.Version, []byte("# hi"))
			return err
		},
		staleClaim: func(t *testing.T, s *Store, ctx context.Context, tile *gridwellv1.Tile) error {
			_, err := s.WriteContent(ctx, tile.Id, tile.Version+7, []byte("# hi"))
			return err
		},
	},
	{
		name: "WriteContent/url address", subject: urlSubject, bumps: true, claims: true,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *gridwellv1.Tile) error {
			_, err := s.WriteContent(ctx, tile.Id, tile.Version, []byte("https://elsewhere.example"))
			return err
		},
		staleClaim: func(t *testing.T, s *Store, ctx context.Context, tile *gridwellv1.Tile) error {
			_, err := s.WriteContent(ctx, tile.Id, tile.Version+7, []byte("https://elsewhere.example"))
			return err
		},
	},
	{
		// A navigation that lands where the row already points writes nothing.
		name: "WriteContent/url address unchanged", subject: urlSubject, bumps: false, claims: true,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *gridwellv1.Tile) error {
			_, err := s.WriteContent(ctx, tile.Id, tile.Version, []byte("https://example.com"))
			return err
		},
		staleClaim: func(t *testing.T, s *Store, ctx context.Context, tile *gridwellv1.Tile) error {
			_, err := s.WriteContent(ctx, tile.Id, tile.Version+7, []byte("https://example.com"))
			return err
		},
	},
	{
		// alt_text is content when the user types it, and the rename latches
		// alt_user.
		name: "RenameTile/user rename", subject: urlSubject, bumps: true, claims: true,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *gridwellv1.Tile) error {
			_, err := s.RenameTile(ctx, tile.Id, tile.Version, "a name I typed")
			return err
		},
		staleClaim: func(t *testing.T, s *Store, ctx context.Context, tile *gridwellv1.Tile) error {
			_, err := s.RenameTile(ctx, tile.Id, tile.Version+7, "a name I typed")
			return err
		},
	},

	// ── Captures: what the server observed. No bump, no claim. ─────────
	{
		// The shell detach path baking in the tmux foreground command.
		name: "SetTileAlt/automatic capture", subject: shellSubject, bumps: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *gridwellv1.Tile) error {
			return s.SetTileAlt(ctx, tile.Id, "vim CLAUDE.md", false)
		},
	},
	{
		name: "SetURLState/freeze capture", subject: urlSubject, bumps: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *gridwellv1.Tile) error {
			_, err := s.SetURLState(ctx, tile.Id, []byte("jpegbytes"), "Example", `["https://example.com"]`)
			return err
		},
	},
	{
		name: "SetShellPreview/frozen frame", subject: shellSubject, bumps: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *gridwellv1.Tile) error {
			_, err := s.SetShellPreview(ctx, tile.Id, []byte("jpegbytes"))
			return err
		},
	},

	// ── Framing: how it looked. No bump, no claim. ─────────────────────
	{
		name: "SetTextView/window and mode", subject: textSubject, bumps: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *gridwellv1.Tile) error {
			_, err := s.SetTextView(ctx, tile.Id, 10, 20, 300, 400, "rendered")
			return err
		},
	},
	{
		name: "SetContentZoom/content scale", subject: shellSubject, bumps: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *gridwellv1.Tile) error {
			_, err := s.SetContentZoom(ctx, tile.Id, zoomOf(1.5))
			return err
		},
	},
	{
		name: "SetFrozen/standing freeze", subject: urlSubject, bumps: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *gridwellv1.Tile) error {
			_, err := s.SetFrozen(ctx, tile.Id, true)
			return err
		},
	},
	{
		name: "SetFraming/doorway viewport", subject: wellSubject, bumps: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *gridwellv1.Tile) error {
			_, err := s.SetFraming(ctx, &gridwellv1.SetFramingRequest{
				TileId: tile.Id,
				Cx:     3, Cy: 4, Zoom: 1.25,
			})
			return err
		},
	},
	{
		// A pane tile's layout is the user's bytes, claimed like a text body.
		name: "SetPaneLayout/workspace arrangement", subject: paneSubject, bumps: true, claims: true,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *gridwellv1.Tile) error {
			_, err := s.SetPaneLayout(ctx, mustParseID(t, tile.Id), tile.Version,
				[]byte(`{"v":1,"root":{"pane":{"id":"p1","zoom":1}},"focus":"p1"}`))
			return err
		},
		staleClaim: func(t *testing.T, s *Store, ctx context.Context, tile *gridwellv1.Tile) error {
			_, err := s.SetPaneLayout(ctx, mustParseID(t, tile.Id), tile.Version+7,
				[]byte(`{"v":1,"root":{"pane":{"id":"p1","zoom":1}},"focus":"p1"}`))
			return err
		},
	},

	// ── Layout: where it sits. No bump, no claim. ──────────────────────
	{
		// A placement claims the placement it saw, not the version: a move
		// never conflicts with typing.
		name: "PlaceTile/move and resize", subject: textSubject, bumps: false, claims: true,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *gridwellv1.Tile) error {
			_, err := s.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
				TileId: tile.Id, GridId: tile.GridId, X: 6, Y: 7, W: 3, H: 3, Claim: placementOf(tile),
			})
			return err
		},
		staleClaim: func(t *testing.T, s *Store, ctx context.Context, tile *gridwellv1.Tile) error {
			seen := placementOf(tile)
			seen.X += 20
			_, err := s.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{
				TileId: tile.Id, GridId: tile.GridId, X: 6, Y: 7, W: 3, H: 3, Claim: seen,
			})
			return err
		},
	},
	{
		// The source row is untouched; the copy carries its version, so the
		// two are the same content until one diverges.
		name: "CloneTile/source row", subject: textSubject, bumps: false,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *gridwellv1.Tile) error {
			_, err := s.CloneTile(ctx, &gridwellv1.CloneTileRequest{
				TileId: tile.Id, DestGridId: tile.GridId, X: 6, Y: 6,
			})
			return err
		},
	},
	{
		// A delete on an ordinary grid moves the row into the trash, same id
		// and same row, so it is layout and the version is untouched.
		// It claims what the writer saw: the content version and the place.
		name: "DeleteTile/move to trash", subject: textSubject, bumps: false, claims: true,
		mutate: func(t *testing.T, s *Store, ctx context.Context, tile *gridwellv1.Tile) error {
			return s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: tile.Id,
				ClaimVersion: tile.Version, Claim: placementOf(tile)})
		},
		staleClaim: func(t *testing.T, s *Store, ctx context.Context, tile *gridwellv1.Tile) error {
			return s.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: tile.Id,
				ClaimVersion: tile.Version + 7, Claim: placementOf(tile)})
		},
	},
}

// A truthful write advances the row version iff it changed content bytes.
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
			got := tileVersion(t, s, tile.Id)
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

// A stale version is refused only by the writes that carry the user's content
// claim; a capture, a framing settle or a raced drag may not become a conflict
// the user has to notice. Most cases have no staleClaim at all, and those are
// counted here so a case cannot quietly grow one back.
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
	// The six content arms, a pane tile's layout among them, the placement
	// and the delete. A change to this number is a change to the rule.
	if claimable != 8 {
		t.Errorf("%d writes can be handed a claim, want 8 — a new one appeared, or one lost its claim", claimable)
	}
}

// A well's view_zoom is the grid viewport, a different fact with its own
// writer, so SetContentZoom refuses it.
func TestContentZoomRefusesWells(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)

	well, err := s.CreateWell(ctx, root, 3, 3, 1, 1, "")
	if err != nil {
		t.Fatalf("CreateWell: %v", err)
	}
	if _, err := s.SetContentZoom(ctx, well.Id, zoomOf(2)); err == nil {
		t.Error("SetContentZoom on a well must be refused")
	}
}
