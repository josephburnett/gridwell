package trash

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEncodePathPercentEncodesButKeepsSlashes pins the .trashinfo Path= encoding:
// each segment is percent-escaped (spaces, '#', '%', unicode) per the spec, but
// the '/' separators are preserved so a file manager can resolve the original.
func TestEncodePathPercentEncodesButKeepsSlashes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/home/joe/notes.txt", "/home/joe/notes.txt"},
		{"/home/joe/a b.txt", "/home/joe/a%20b.txt"},
		{"/home/joe/weird#name%.txt", "/home/joe/weird%23name%25.txt"},
		{"/tmp/café/résumé", "/tmp/caf%C3%A9/r%C3%A9sum%C3%A9"},
	}
	for _, c := range cases {
		if got := encodePath(c.in); got != c.want {
			t.Errorf("encodePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestIntoMissingSourceErrors: trashing a path that doesn't exist is an error
// (Lstat fails) and writes nothing — no orphaned .trashinfo is left behind.
func TestIntoMissingSourceErrors(t *testing.T) {
	trashDir := t.TempDir()
	src := filepath.Join(t.TempDir(), "ghost.txt")
	if err := into(trashDir, src); err == nil {
		t.Fatal("trashing a nonexistent path should error")
	}
	if entries, _ := os.ReadDir(filepath.Join(trashDir, "info")); len(entries) != 0 {
		t.Errorf("a failed trash left %d orphaned info record(s)", len(entries))
	}
}

// TestHomeDirHonorsXDG: the home-trash dir is $XDG_DATA_HOME/Trash when set,
// else ~/.local/share/Trash.
func TestHomeDirHonorsXDG(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/xdg/data")
	if got, err := homeDir(); err != nil || got != "/xdg/data/Trash" {
		t.Errorf("homeDir() with XDG = %q, %v; want /xdg/data/Trash", got, err)
	}

	t.Setenv("XDG_DATA_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	want := filepath.Join(home, ".local", "share", "Trash")
	if got, err := homeDir(); err != nil || got != want {
		t.Errorf("homeDir() default = %q, %v; want %q", got, want, err)
	}
}

// TestCopyTreePreservesFilesDirsAndSymlinks exercises the cross-device fallback
// (copyTree) directly: a tree with a nested file, a subdir, and a symlink copies
// byte-for-byte with the symlink kept as a symlink (not dereferenced).
func TestCopyTreePreservesFilesDirsAndSymlinks(t *testing.T) {
	src := filepath.Join(t.TempDir(), "tree")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "sub", "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub/a.txt", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "copy")
	if err := copyTree(src, dst); err != nil {
		t.Fatalf("copyTree: %v", err)
	}

	if data, err := os.ReadFile(filepath.Join(dst, "sub", "a.txt")); err != nil || string(data) != "hello" {
		t.Errorf("nested file = %q, %v; want hello", data, err)
	}
	fi, err := os.Lstat(filepath.Join(dst, "link"))
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("symlink not preserved as a symlink: mode=%v err=%v", fi.Mode(), err)
	}
	if target, err := os.Readlink(filepath.Join(dst, "link")); err != nil || target != "sub/a.txt" {
		t.Errorf("symlink target = %q, %v; want sub/a.txt", target, err)
	}
}

// TestTrashFileMovesAndRecords: trashing a file moves it under the trash
// files/ dir and writes a .trashinfo record pointing back at the original
// absolute path — i.e. it is recoverable, not gone.
func TestTrashFileMovesAndRecords(t *testing.T) {
	src := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(src, []byte("keepme"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	if err := into(dir, src); err != nil {
		t.Fatalf("trash: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Errorf("source still present after trash (err=%v)", err)
	}
	moved := filepath.Join(dir, "files", "notes.txt")
	data, err := os.ReadFile(moved)
	if err != nil {
		t.Fatalf("trashed file missing: %v", err)
	}
	if string(data) != "keepme" {
		t.Errorf("trashed content = %q, want keepme", data)
	}
	info, err := os.ReadFile(filepath.Join(dir, "info", "notes.txt.trashinfo"))
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
// so deleting a directory is recoverable rather than rm -rf.
func TestTrashDirMovesWholeTree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	trashDir := t.TempDir()

	if err := into(trashDir, dir); err != nil {
		t.Fatalf("trash dir: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("source dir still present")
	}
	if _, err := os.Stat(filepath.Join(trashDir, "files", "project", "sub", "a.txt")); err != nil {
		t.Errorf("nested file not preserved in trash: %v", err)
	}
}

// TestTrashNameCollisionDisambiguates: trashing two same-named files keeps
// both — the second is suffixed .2 — so a delete never silently clobbers a
// previously trashed item.
func TestTrashNameCollisionDisambiguates(t *testing.T) {
	trashDir := t.TempDir()
	for _, content := range []string{"first", "second"} {
		d := t.TempDir()
		src := filepath.Join(d, "dup.txt")
		if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := into(trashDir, src); err != nil {
			t.Fatalf("trash %q: %v", content, err)
		}
	}
	if _, err := os.Stat(filepath.Join(trashDir, "files", "dup.txt")); err != nil {
		t.Errorf("first trashed file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(trashDir, "files", "dup.txt.2")); err != nil {
		t.Errorf("second trashed file not disambiguated: %v", err)
	}
}
