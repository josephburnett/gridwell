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
	default:
		t.Fatal("no event received")
	}
}
