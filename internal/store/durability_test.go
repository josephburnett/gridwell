package store

import (
	"context"
	"reflect"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// TestSynchronousPinned confirms Open pins PRAGMA synchronous to NORMAL (1).
// synchronous is connection-scoped and defaults to FULL regardless of journal
// mode, so the value must be set on every Open; this guards against the pragma
// being dropped or the WAL durability tradeoff silently changing. File-backed
// because a ":memory:" DB does not honor synchronous.
func TestSynchronousPinned(t *testing.T) {
	s, _ := newTestStoreFile(t)
	v, err := readPragmaInt(context.Background(), s.db, "synchronous")
	if err != nil {
		t.Fatalf("read synchronous: %v", err)
	}
	const synchronousNormal = 1
	if v != synchronousNormal {
		t.Errorf("PRAGMA synchronous = %d, want %d (NORMAL)", v, synchronousNormal)
	}
}

// TestReopenRoundTrip is the core durability proof: data written by one Open
// survives Close and a second Open of the same file byte-for-byte — same rows,
// same ids, same versions, same content bytes, same header version. This is the
// "lasts forever across a restart" guarantee, and the only store test that
// exercises real on-disk WAL (newTestStore uses :memory:).
func TestReopenRoundTrip(t *testing.T) {
	s, path := newTestStoreFile(t)
	ctx := context.Background()
	root := rootID(t, s)

	txt, err := s.CreateText(ctx, &rpc.CreateTextRequest{
		Path: rpc.Path{}, GridID: root, X: 0, Y: 0, W: 1, H: 1, Data: []byte("# hello world"),
	})
	if err != nil {
		t.Fatalf("create text: %v", err)
	}
	if _, err := s.CreateURL(ctx, &rpc.CreateURLRequest{
		Path: rpc.Path{}, GridID: root, X: 2, Y: 0, W: 1, H: 1, URL: "https://example.com",
	}); err != nil {
		t.Fatalf("create url: %v", err)
	}
	if _, err := s.CreateWell(ctx, &rpc.CreateWellRequest{
		Path: rpc.Path{}, GridID: root, X: 4, Y: 0, W: 1, H: 1,
	}); err != nil {
		t.Fatalf("create well: %v", err)
	}

	before := snapshotDB(t, s, root, txt.BlobID)
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen %s: %v", path, err)
	}
	defer reopened.Close()
	after := snapshotDB(t, reopened, root, txt.BlobID)

	if !reflect.DeepEqual(before, after) {
		t.Errorf("state changed across reopen:\n before = %+v\n after  = %+v", before, after)
	}
}

// dbState is the observable persisted state snapshotDB captures: the root grid
// and its tiles, a text blob's bytes + media type, and the schema header
// version. Compared before/after a reopen to prove nothing drifted.
type dbState struct {
	userVersion int64
	grid        rpc.Grid
	tiles       []rpc.Tile
	textBytes   []byte
	textMedia   string
}

func snapshotDB(t *testing.T, s *Store, gridID string, textBlobID int64) dbState {
	t.Helper()
	ctx := context.Background()
	g, err := s.GetGrid(ctx, gridID)
	if err != nil {
		t.Fatalf("get grid %s: %v", gridID, err)
	}
	data, media, err := s.GetBlobWithMedia(ctx, textBlobID)
	if err != nil {
		t.Fatalf("get blob %d: %v", textBlobID, err)
	}
	uv, err := readPragmaInt(ctx, s.db, "user_version")
	if err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	return dbState{userVersion: uv, grid: g.Grid, tiles: g.Tiles, textBytes: data, textMedia: media}
}
