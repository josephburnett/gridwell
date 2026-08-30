package store

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

// The content-stream suite (2026-07-26 redesign): WriteContent/ReadContent
// are the one way content bytes move, and the version-semantics table is
// kind-determined in the store — text bumps, pane layout never does.

func TestWriteContentTextBumpsAndPairsWithRead(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	tile := placeText(t, s, root, 0, 0)

	got, err := s.WriteContent(ctx, tile.ID, tile.Version, []byte("# New Title\n\nbody"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if got.Version <= tile.Version {
		t.Errorf("text content edit must bump version: %d -> %d", tile.Version, got.Version)
	}
	if got.AltText != "New Title" {
		t.Errorf("alt derives from the first line: got %q", got.AltText)
	}

	data, media, version, err := s.ReadContent(ctx, tile.ID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(data, []byte("# New Title\n\nbody")) {
		t.Errorf("read bytes = %q", data)
	}
	if version != got.Version {
		t.Errorf("bytes paired with version %d, tile is at %d — the save basis must never split", version, got.Version)
	}
	if media == "" {
		t.Error("media type must ride along (blobs are self-describing)")
	}
}

func TestWriteContentPaneLayoutNeverBumps(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	pane, err := s.CreatePane(ctx, root, 0, 0, 1, 1, "ws", nil)
	if err != nil {
		t.Fatalf("create pane: %v", err)
	}

	got, err := s.WriteContent(ctx, pane.ID, pane.Version, []byte(`{"v":1}`))
	if err != nil {
		t.Fatalf("write layout: %v", err)
	}
	if got.Version != pane.Version {
		t.Errorf("pane layout is framing-class: version %d must stay %d", got.Version, pane.Version)
	}
}

func TestWriteContentRefusesKindsWithoutContent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	well := placeWell(t, s, root, 0, 0)

	if _, err := s.WriteContent(ctx, well.ID, well.Version, []byte("x")); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("well: got %v, want ErrInvalidArgument", err)
	}
}

// Issue #209 (drop first, prompt on first descent): a url tile's ADDRESS is
// its content — the tile is created empty at drop and the address arrives as
// a versioned WriteContent at the first-descent prompt. Changing where a
// tile points is a content edit and bumps.
func TestWriteContentURLSetsAddress(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)

	// An address-less url tile is the legal unconfigured state.
	url, err := s.CreateURL(ctx, &rpc.CreateURLRequest{GridID: root, X: 0, Y: 0, W: 1, H: 1})
	if err != nil {
		t.Fatalf("create empty url: %v", err)
	}
	if url.URLString != "" {
		t.Errorf("unconfigured url tile: URLString = %q, want empty", url.URLString)
	}

	got, err := s.WriteContent(ctx, url.ID, url.Version, []byte("https://example.com"))
	if err != nil {
		t.Fatalf("write address: %v", err)
	}
	if got.Version <= url.Version {
		t.Errorf("the address write is a content edit — version %d must bump past %d", got.Version, url.Version)
	}
	if got.URLString != "https://example.com" {
		t.Errorf("URLString = %q", got.URLString)
	}

	// Read pairs the address with the row version (the save-basis contract).
	data, _, version, err := s.ReadContent(ctx, url.ID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(data) != "https://example.com" || version != got.Version {
		t.Errorf("read = (%q, v%d), want the address at v%d", data, version, got.Version)
	}

	// Garbage refused loudly; the old address stays byte-for-byte intact.
	if _, err := s.WriteContent(ctx, got.ID, got.Version, []byte("javascript:alert(1)")); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("non-http scheme: got %v, want ErrInvalidArgument", err)
	}
	// So is an EMPTY write — configuring must produce a real address.
	if _, err := s.WriteContent(ctx, got.ID, got.Version, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("empty address write: got %v, want ErrInvalidArgument", err)
	}
	// Stale claim refused.
	if _, err := s.WriteContent(ctx, got.ID, got.Version+7, []byte("https://other.example")); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale claim: got %v, want ErrVersionConflict", err)
	}
	after, err := s.GetTile(ctx, got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.URLString != "https://example.com" || after.Version != got.Version {
		t.Errorf("refused writes mutated the row: %q v%d", after.URLString, after.Version)
	}
}

func TestWriteContentLinkRefused(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	link, err := s.CreateLeafLink(ctx, root, 0, 0, 1, 1,
		rpc.KindText, "aabbccddaabbccddaabbccddaabbccdd/9", "linked")
	if err != nil {
		t.Fatalf("create leaf link: %v", err)
	}

	_, err = s.WriteContent(ctx, link.ID, link.Version, []byte("stomp"))
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("a link owns no content — write must be refused, got %v", err)
	}
}

func TestRenameTileVersionedAndLatches(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	root := rootID(t, s)
	url, err := s.CreateURL(ctx, &rpc.CreateURLRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1, URL: "https://example.com",
	})
	if err != nil {
		t.Fatalf("create url: %v", err)
	}

	// Stale claim refused — the rename is a real user edit now.
	if _, err := s.RenameTile(ctx, url.ID, url.Version+7, "My Page"); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale rename: got %v, want ErrVersionConflict", err)
	}

	renamed, err := s.RenameTile(ctx, url.ID, url.Version, "My Page")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if renamed.AltText != "My Page" {
		t.Errorf("alt = %q, want My Page", renamed.AltText)
	}

	// A later automatic title capture must defer to the user-owned name.
	after, err := s.SetURLState(ctx, &rpc.SetURLStateRequest{
		TileID: url.ID, Version: renamed.Version, Title: "Captured Page Title",
	})
	if err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if after.AltText != "My Page" {
		t.Errorf("capture clobbered the user rename: alt = %q", after.AltText)
	}

	// Text tiles derive their name from content; rename is refused.
	text := placeText(t, s, root, 5, 5)
	if _, err := s.RenameTile(ctx, text.ID, text.Version, "nope"); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("text rename: got %v, want ErrInvalidArgument", err)
	}
}
