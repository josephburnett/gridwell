package store

import (
	"context"
	"errors"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
)

func TestGetBlobReturnsBytes(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()

	f, err := s.CreateFile(ctx, &rpc.CreateFileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: root,
		X: 0, Y: 0, W: 1, H: 1, MimeType: "text/markdown", Data: []byte("payload"),
	})
	if err != nil {
		t.Fatal(err)
	}

	data, mime, err := s.GetBlob(ctx, f.BlobID)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "payload" {
		t.Errorf("data = %q", data)
	}
	if mime != "text/markdown" {
		t.Errorf("mime = %q", mime)
	}

	if _, _, err := s.GetBlob(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: got %v", err)
	}
}

func TestSubscribeEventsReceivesPublish(t *testing.T) {
	s := newTestStore(t)
	root := rootID(t, s)
	ctx := context.Background()
	ch, cancel := s.SubscribeEvents()
	defer cancel()
	if _, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: root, X: 0, Y: 0, W: 1, H: 1,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-ch:
		if ev.Kind != rpc.EventTileChanged {
			t.Errorf("kind = %v, want NodeChanged", ev.Kind)
		}
	default:
		t.Fatal("no event received")
	}
}
