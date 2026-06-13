package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTrashFileMovesAndRecords: trashing a file moves it under the trash
// files/ dir and writes a .trashinfo record pointing back at the original
// absolute path — i.e. it is recoverable, not gone.
func TestTrashFileMovesAndRecords(t *testing.T) {
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("keepme"), 0o644); err != nil {
		t.Fatal(err)
	}
	trash := t.TempDir()

	if err := trashFileInto(trash, src); err != nil {
		t.Fatalf("trash: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source still present after trash (err=%v)", err)
	}
	moved := filepath.Join(trash, "files", "notes.txt")
	data, err := os.ReadFile(moved)
	if err != nil {
		t.Fatalf("trashed file missing: %v", err)
	}
	if string(data) != "keepme" {
		t.Errorf("trashed content = %q, want keepme", data)
	}
	info, err := os.ReadFile(filepath.Join(trash, "info", "notes.txt.trashinfo"))
	if err != nil {
		t.Fatalf("trashinfo missing: %v", err)
	}
	s := string(info)
	if !strings.Contains(s, "[Trash Info]") || !strings.Contains(s, "Path=") || !strings.Contains(s, "DeletionDate=") {
		t.Errorf("malformed trashinfo:\n%s", s)
	}
	if !strings.Contains(s, src) {
		t.Errorf("trashinfo Path doesn't reference original %q:\n%s", src, s)
	}
}

// TestTrashDirMovesWholeTree: trashing a directory moves the entire tree,
// so a file-well blackhole delete is recoverable rather than rm -rf.
func TestTrashDirMovesWholeTree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	trash := t.TempDir()

	if err := trashFileInto(trash, dir); err != nil {
		t.Fatalf("trash dir: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("source dir still present")
	}
	if _, err := os.Stat(filepath.Join(trash, "files", "project", "sub", "a.txt")); err != nil {
		t.Errorf("nested file not preserved in trash: %v", err)
	}
}

// TestTrashNameCollisionDisambiguates: trashing two same-named files keeps
// both — the second is suffixed .2 — so a delete never silently clobbers a
// previously trashed item.
func TestTrashNameCollisionDisambiguates(t *testing.T) {
	trash := t.TempDir()
	for _, content := range []string{"first", "second"} {
		d := t.TempDir()
		src := filepath.Join(d, "dup.txt")
		if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := trashFileInto(trash, src); err != nil {
			t.Fatalf("trash %q: %v", content, err)
		}
	}
	if _, err := os.Stat(filepath.Join(trash, "files", "dup.txt")); err != nil {
		t.Errorf("first trashed file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(trash, "files", "dup.txt.2")); err != nil {
		t.Errorf("second trashed file not disambiguated: %v", err)
	}
}
