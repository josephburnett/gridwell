package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/internal/rpc"
)

func TestGetBlobReturnsBytes(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	f, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		Path: rpc.Path{}, GridID: root,
		X: 0, Y: 0, W: 1, H: 1, Data: []byte("payload"),
	})
	if err != nil {
		t.Fatal(err)
	}

	data, err := s.GetBlob(ctx, f.BlobID)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "payload" {
		t.Errorf("data = %q", data)
	}

	if _, err := s.GetBlob(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: got %v", err)
	}
}

// blobMedia reads back a blob's bytes and self-describing media type through
// the public read path — the same route GetTileContent uses to report a type
// instead of hard-coding one.
func blobMedia(t *testing.T, s *Store, id int64) (data []byte, mediaType string) {
	t.Helper()
	data, mediaType, err := s.GetBlobWithMedia(context.Background(), id)
	if err != nil {
		t.Fatalf("read blob media id=%d: %v", id, err)
	}
	return data, mediaType
}

// TestBlobSelfDescribing confirms text content blobs are stamped text/markdown
// and frozen previews image/jpeg, read back through GetBlobWithMedia, so a blob
// is interpretable independent of the column that references it.
func TestBlobSelfDescribing(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	txt, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1, Data: []byte("# hi"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, mt := blobMedia(t, s, txt.BlobID); mt != mediaMarkdown {
		t.Errorf("text blob media = %q, want %q", mt, mediaMarkdown)
	}

	url, err := s.CreateURL(ctx, &rpc.CreateURLRequest{
		Path: rpc.Path{}, GridID: root, X: 2, Y: 0, W: 1, H: 1, URL: "https://example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := s.SetURLState(ctx, &rpc.SetURLStateRequest{
		Path: rpc.Path{}, TileID: url.ID, Version: url.Version, JPEG: []byte{0xFF, 0xD8, 0xFF},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, mt := blobMedia(t, s, frozen.PreviewBlobID); mt != mediaJPEG {
		t.Errorf("preview blob media = %q, want %q", mt, mediaJPEG)
	}
}

// TestGridUpdatedAtStamped confirms a structural mutation moves the parent
// grid's updated_at (the recency signal the future "jump to recent" feature
// will read).
func TestGridUpdatedAtStamped(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	gridUpdatedAt := func() int64 {
		var v int64
		if err := s.db.QueryRow(`SELECT updated_at FROM grids WHERE id = ?`, root).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	s.SetClock(func() time.Time { return time.Unix(1000, 0) })
	if _, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1, Data: []byte("a"),
	}); err != nil {
		t.Fatal(err)
	}
	if got := gridUpdatedAt(); got != 1000 {
		t.Errorf("grid updated_at = %d, want 1000", got)
	}
}

func TestSubscribeEventsReceivesPublish(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if _, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-ch:
		if ev.Kind != rpc.EventTileChanged {
			t.Errorf("kind = %v, want TileChanged", ev.Kind)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event received")
	}
}
