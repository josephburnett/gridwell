package fssource

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadSortsAndClassifies(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "alpha.md"), "hello")
	mustWrite(t, filepath.Join(root, "beta.bin"), "\x00\x01\x02\x03")
	if err := os.Mkdir(filepath.Join(root, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("len(entries)=%d, want 3", len(got))
	}
	// Sorted alphabetically.
	wantNames := []string{"alpha.md", "beta.bin", "subdir"}
	for i, e := range got {
		if e.Name != wantNames[i] {
			t.Errorf("entries[%d].Name = %q, want %q", i, e.Name, wantNames[i])
		}
	}
	if got[0].Kind != KindFile || got[1].Kind != KindFile || got[2].Kind != KindDir {
		t.Errorf("kinds = %v/%v/%v, want file/file/dir",
			got[0].Kind, got[1].Kind, got[2].Kind)
	}
	if got[0].Size != int64(len("hello")) {
		t.Errorf("alpha.md size = %d, want 5", got[0].Size)
	}
	if got[2].AbsPath != filepath.Join(root, "subdir") {
		t.Errorf("subdir AbsPath = %q", got[2].AbsPath)
	}
}

func TestReadFollowsSymlinks(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "real.txt")
	mustWrite(t, target, "abc")
	link := filepath.Join(root, "alias.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unsupported")
	}
	got, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	var alias *Entry
	for i := range got {
		if got[i].Name == "alias.txt" {
			alias = &got[i]
		}
	}
	if alias == nil {
		t.Fatal("alias.txt not found")
	}
	if !alias.IsSymlink {
		t.Error("expected IsSymlink=true")
	}
	if alias.IsBrokenSymlink {
		t.Error("not broken")
	}
	if alias.Kind != KindFile {
		t.Errorf("alias kind = %q, want file", alias.Kind)
	}
}

func TestReadDetectsBrokenSymlink(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "missing"), link); err != nil {
		t.Skip("symlinks unsupported")
	}
	got, err := Read(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].IsBrokenSymlink {
		t.Errorf("expected single broken-symlink entry, got %+v", got)
	}
}

func TestReadNonexistentDirReturnsError(t *testing.T) {
	if _, err := Read("/this/path/should/not/exist/anywhere"); err == nil {
		t.Error("expected error")
	}
}

func TestMetadataMarkdownIsDeterministic(t *testing.T) {
	e := Entry{
		Name:    "foo.md",
		AbsPath: "/x/foo.md",
		Kind:    KindFile,
		Size:    42,
		ModTime: time.Unix(1700000000, 0),
	}
	a := MetadataMarkdown(e)
	b := MetadataMarkdown(e)
	if a != b {
		t.Error("MetadataMarkdown should be deterministic")
	}
	if !strings.Contains(a, "foo.md") || !strings.Contains(a, "42 bytes") {
		t.Errorf("missing fields:\n%s", a)
	}
}

func TestMetadataMarkdownDirectoryLabel(t *testing.T) {
	e := Entry{Name: "bin", Kind: KindDir, AbsPath: "/bin"}
	md := MetadataMarkdown(e)
	if !strings.Contains(md, "directory") {
		t.Errorf("expected directory label, got:\n%s", md)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
