package store

import (
	"context"
	"errors"
	"testing"

	"github.com/josephburnett/ascent/internal/rpc"
)

func TestGetBlobChecksPermissionAndReturnsBytes(t *testing.T) {
	s := newTestStore(t)
	u := fixtureUser(t, s)
	ctx := context.Background()

	f, err := s.CreateFile(ctx, u.ID, &rpc.CreateFileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID,
		X: 0, Y: 0, W: 1, H: 1, MimeType: "text/markdown", Data: []byte("payload"),
	})
	if err != nil {
		t.Fatal(err)
	}

	data, mime, err := s.GetBlob(ctx, u.ID, f.BlobID)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "payload" {
		t.Errorf("data = %q", data)
	}
	if mime != "text/markdown" {
		t.Errorf("mime = %q", mime)
	}

	// Unknown blob.
	if _, _, err := s.GetBlob(ctx, u.ID, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: got %v", err)
	}
}

func TestGetBlobDeniedWhenNoReadableNode(t *testing.T) {
	s := newTestStore(t)
	u := fixtureUser(t, s)
	other, err := s.CreateUser(context.Background(), "bob", "p")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	f, err := s.CreateFile(ctx, u.ID, &rpc.CreateFileRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID,
		X: 0, Y: 0, W: 1, H: 1, MimeType: "text/markdown", Data: []byte("private"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// bob is not in alice's primary group and the default mode (0o640) gives
	// "other" no read.
	if _, _, err := s.GetBlob(ctx, other.ID, f.BlobID); !errors.Is(err, ErrPermissionDenied) {
		t.Errorf("expected ErrPermissionDenied, got %v", err)
	}
}

func TestSubscribeEventsReceivesPublish(t *testing.T) {
	s := newTestStore(t)
	u := fixtureUser(t, s)
	ctx := context.Background()
	ch, cancel := s.SubscribeEvents(u.ID)
	defer cancel()
	if _, err := s.CreateWell(ctx, u.ID, &rpc.CreateWellRequest{
		Path: rpc.Path{}, ViewRect: largeView(), GridID: u.RootGridID, X: 0, Y: 0, W: 1, H: 1,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-ch:
		if ev.Kind != rpc.EventNodeChanged {
			t.Errorf("kind = %v, want NodeChanged", ev.Kind)
		}
	default:
		t.Fatal("no event received")
	}
}
