package store

import (
	"bytes"
	"context"
	"testing"

	"google.golang.org/protobuf/proto"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// Open pins PRAGMA synchronous to NORMAL. It is connection-scoped and defaults
// to FULL, so it must be set on every Open. File-backed, because a ":memory:"
// DB does not honor synchronous.
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

// The core durability proof: data written by one Open survives Close and a
// second Open of the same file byte-for-byte. The only store test that
// exercises real on-disk WAL, newTestStore being :memory:.
func TestReopenRoundTrip(t *testing.T) {
	s, path := newTestStoreFile(t)
	ctx := context.Background()
	root := rootID(t, s)

	txt, err := s.CreateText(ctx, root, 0, 0, 1, 1, []byte("# hello world"))
	if err != nil {
		t.Fatalf("create text: %v", err)
	}
	if _, err := s.CreateURL(ctx, root, 2, 0, 1, 1, "https://example.com"); err != nil {
		t.Fatalf("create url: %v", err)
	}
	if _, err := s.CreateDoor(ctx, root, 4, 0, 1, 1, ""); err != nil {
		t.Fatalf("create door: %v", err)
	}

	before := snapshotDB(t, s, root, txt.BlobId)
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen %s: %v", path, err)
	}
	defer reopened.Close()
	after := snapshotDB(t, reopened, root, txt.BlobId)

	if !before.same(after) {
		t.Errorf("state changed across reopen:\n before = %+v\n after  = %+v", before, after)
	}
}

// dbState is the observable persisted state snapshotDB captures: the root grid
// and its tiles, a text blob's bytes + media type, and the schema header
// version. Compared before/after a reopen to prove nothing drifted.
type dbState struct {
	userVersion int64
	grid        *gridwellv1.GetGridResponse
	textBytes   []byte
	textMedia   string
}

// same compares two snapshots. The grid arm goes through proto.Equal: a
// generated message carries reflection bookkeeping that a structural compare
// would read as a difference.
func (d dbState) same(o dbState) bool {
	return d.userVersion == o.userVersion && proto.Equal(d.grid, o.grid) &&
		bytes.Equal(d.textBytes, o.textBytes) && d.textMedia == o.textMedia
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
	return dbState{userVersion: uv, grid: g, textBytes: data, textMedia: media}
}
