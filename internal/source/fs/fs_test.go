package fs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/internal/source"
)

// recordHost records remove calls instead of touching disk, so deletion
// tests never rm real files.
type recordHost struct {
	removed    []string
	removedAll []string
}

func (h *recordHost) Remove(p string) error    { h.removed = append(h.removed, p); return nil }
func (h *recordHost) RemoveAll(p string) error { h.removedAll = append(h.removedAll, p); return nil }

func tempTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestAttach(t *testing.T) {
	s := New(nil)
	att, err := s.Attach(context.Background(), map[string]string{"path": "/home/joe"})
	if err != nil {
		t.Fatal(err)
	}
	if att.RootSourceID != "/home/joe" {
		t.Errorf("root = %q, want /home/joe", att.RootSourceID)
	}
	if att.Label != "joe" {
		t.Errorf("label = %q, want joe", att.Label)
	}
	if att.HasSession {
		t.Error("fs should not advertise a session")
	}

	root, err := s.Attach(context.Background(), map[string]string{"path": "/"})
	if err != nil {
		t.Fatal(err)
	}
	if root.Label != "files" {
		t.Errorf("root label = %q, want files", root.Label)
	}

	if _, err := s.Attach(context.Background(), map[string]string{}); err == nil {
		t.Error("empty path should error")
	}
}

func TestListProjectsEntries(t *testing.T) {
	dir := tempTree(t)
	s := New(nil)
	lst, err := s.List(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !lst.Authoritative {
		t.Error("fs listing must be authoritative")
	}
	byKey := map[string]source.Node{}
	for _, n := range lst.Nodes {
		byKey[n.Key] = n
	}
	if len(byKey) != 2 {
		t.Fatalf("got %d nodes, want 2: %+v", len(byKey), lst.Nodes)
	}

	sub, ok := byKey["sub"]
	if !ok || sub.Kind != source.KindWell {
		t.Fatalf("sub: %+v, want a well", sub)
	}
	if sub.Child != filepath.Join(dir, "sub") {
		t.Errorf("sub.Child = %q, want %q", sub.Child, filepath.Join(dir, "sub"))
	}

	note, ok := byKey["note.txt"]
	if !ok || note.Kind != source.KindText {
		t.Fatalf("note.txt: %+v, want text", note)
	}
	if note.Body == nil || note.Body.MediaType != "text/markdown" {
		t.Fatalf("note.Body = %+v, want a markdown ref", note.Body)
	}
	if note.Body.BlobRef != filepath.Join(dir, "note.txt") {
		t.Errorf("note.Body.BlobRef = %q", note.Body.BlobRef)
	}
	if note.Body.Hash == "" {
		t.Error("note.Body.Hash should be set for dedup")
	}
	if !note.Caps.Delete {
		t.Error("file node should be deletable")
	}
}

func TestListMissingDirIsEmptyNotError(t *testing.T) {
	s := New(nil)
	lst, err := s.List(context.Background(), filepath.Join(t.TempDir(), "gone"))
	if err != nil {
		t.Fatalf("missing dir should not error: %v", err)
	}
	if len(lst.Nodes) != 0 || !lst.Authoritative {
		t.Errorf("missing dir = %+v, want empty authoritative", lst)
	}
}

func TestReadBlobRegeneratesMetadata(t *testing.T) {
	dir := tempTree(t)
	s := New(nil)
	path := filepath.Join(dir, "note.txt")

	// The hash from List must match the bytes from ReadBlob (dedup contract).
	lst, _ := s.List(context.Background(), dir)
	var wantHash string
	for _, n := range lst.Nodes {
		if n.Key == "note.txt" {
			wantHash = n.Body.Hash
		}
	}

	body, err := s.ReadBlob(context.Background(), dir, path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "# note.txt") {
		t.Errorf("body missing heading:\n%s", body)
	}
	if got := hashHex(body); got != wantHash {
		t.Errorf("ReadBlob hash %s != List hash %s — dedup would break", got, wantHash)
	}
}

func TestProbe(t *testing.T) {
	dir := tempTree(t)
	s := New(nil)
	if p, _ := s.Probe(context.Background(), dir, "note.txt"); p != source.PresencePresent {
		t.Errorf("present file = %v, want present", p)
	}
	if p, _ := s.Probe(context.Background(), dir, "ghost"); p != source.PresenceGone {
		t.Errorf("missing file = %v, want gone", p)
	}
}

func TestDeleteFileAndDir(t *testing.T) {
	dir := tempTree(t)
	h := &recordHost{}
	s := New(h)

	settled, err := s.Delete(context.Background(), dir, "note.txt", 0)
	if err != nil || !settled {
		t.Fatalf("delete file: settled=%v err=%v", settled, err)
	}
	if len(h.removed) != 1 || h.removed[0] != filepath.Join(dir, "note.txt") {
		t.Errorf("removed = %v", h.removed)
	}

	settled, err = s.Delete(context.Background(), dir, "sub", 0)
	if err != nil || !settled {
		t.Fatalf("delete dir: settled=%v err=%v", settled, err)
	}
	if len(h.removedAll) != 1 || h.removedAll[0] != filepath.Join(dir, "sub") {
		t.Errorf("removedAll = %v", h.removedAll)
	}
}

func TestDeleteMissingIsSettled(t *testing.T) {
	s := New(&recordHost{})
	settled, err := s.Delete(context.Background(), t.TempDir(), "ghost", 0)
	if err != nil || !settled {
		t.Errorf("deleting a missing entry should be a no-op success: settled=%v err=%v", settled, err)
	}
}

func TestMutationsUnsupported(t *testing.T) {
	s := New(nil)
	ctx := context.Background()
	if _, err := s.Move(ctx, source.MoveRequest{}); err != source.ErrUnsupported {
		t.Errorf("Move err = %v", err)
	}
	if _, err := s.Clone(ctx, source.CloneRequest{}); err != source.ErrUnsupported {
		t.Errorf("Clone err = %v", err)
	}
	if _, err := s.Write(ctx, "", "", 0, nil); err != source.ErrUnsupported {
		t.Errorf("Write err = %v", err)
	}
	if _, err := s.GetSession(ctx, ""); err != source.ErrUnsupported {
		t.Errorf("GetSession err = %v", err)
	}
}
