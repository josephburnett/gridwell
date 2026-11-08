package files

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/api/rpc"
)

// bodyOf is body of the file at path.
func bodyOf(t *testing.T, path string) ([]byte, string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return body(f, fi)
}

// previewOf is previewJPEG of the file at path, nil for none there.
func previewOf(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return previewJPEG(f, fi)
}

// A text file past the cap shows its first bytes in its presentation: a long
// log reads as its beginning, not as a summary of itself.
func TestABodyPastTheCapIsTheFilesBeginning(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"big.log", "big.md"} {
		content := bytes.Repeat([]byte("0123456789abcdef"), bodyCap/16+1)
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, content, 0o644); err != nil {
			t.Fatal(err)
		}
		if data, _ := bodyOf(t, p); !bytes.Equal(data, content[:bodyCap]) {
			t.Errorf("body(%s) = %d bytes, want its first %d", name, len(data), bodyCap)
		}
	}
}

// Every name has a presentation, and its body is the media type that names.
func TestABodyIsWhatItsPresentationSays(t *testing.T) {
	dir := t.TempDir()
	want := map[string]string{
		"notes.md": rpc.TextPresentationBoth, "plan.org": rpc.TextPresentationBoth, "app.log": rpc.TextPresentationPlain,
		"Makefile": rpc.TextPresentationPlain, "blob.bin": rpc.TextPresentationBoth, "untitled": rpc.TextPresentationBoth,
		"index.html": rpc.TextPresentationPlain,
	}
	media := map[string]string{rpc.TextPresentationPlain: "text/plain", rpc.TextPresentationBoth: "text/markdown"}
	for name, p := range want {
		if got := presentation(name); got != p {
			t.Errorf("presentation(%q) = %q, want %q", name, got, p)
		}
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if data, mt := bodyOf(t, path); data == nil || mt != media[p] {
			t.Errorf("body(%q) = %q in %q, want %q", name, data, mt, media[p])
		}
	}
}

// Two saves of a picture within a second are two pictures.
func TestTwoSavesInASecondAreTwoPreviewStamps(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pic.png")
	if err := os.WriteFile(p, []byte("\x89PNG"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := time.Unix(1_700_000_000, 0)
	stamps := map[int64]bool{}
	for _, at := range []time.Time{second.Add(100 * time.Millisecond), second.Add(600 * time.Millisecond)} {
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
		stamps[previewStamp(p)] = true
	}
	if len(stamps) != 2 {
		t.Fatalf("stamps %v, want two", stamps)
	}
}

// An image's picture is a JPEG bounded on its longest edge.
func TestAnImagesPictureIsBounded(t *testing.T) {
	p := filepath.Join(t.TempDir(), "wide.png")
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2048, 64))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	got := previewOf(t, p)
	img, _, err := image.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != previewEdge || b.Dy() != 16 {
		t.Errorf("picture %v", b)
	}
	if previewOf(t, filepath.Join(filepath.Dir(p), "notes.md")) != nil {
		t.Error("a text file has a picture")
	}
}

// A small file declaring a vast image has no picture, and decoding none of
// it costs nothing: the pixel budget is read from its header.
func TestAnImagePastThePixelBudgetIsNotDecoded(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	// IHDR's width and height, then its CRC over the chunk type and data.
	binary.BigEndian.PutUint32(b[16:], 8000)
	binary.BigEndian.PutUint32(b[20:], 8000)
	binary.BigEndian.PutUint32(b[29:], crc32.ChecksumIEEE(b[12:29]))
	p := filepath.Join(t.TempDir(), "bomb.png")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got := previewOf(t, p)
	runtime.ReadMemStats(&after)
	if got != nil {
		t.Error("an image past the budget has a picture")
	}
	if n := after.TotalAlloc - before.TotalAlloc; n > 8<<20 {
		t.Errorf("previewJPEG allocated %d MiB for a %d-byte file", n>>20, len(b))
	}
}

// underRoot has no root "/" edge.
func TestUnderRoot(t *testing.T) {
	for _, c := range []struct {
		root, path string
		ok         bool
	}{
		{"/", "/.nofollow", true}, {"/", "/", true}, {"/home/joe", "/home/joe", true},
		{"/home/joe", "/home/joe/sub/a.md", true}, {"/home/joe", "/home/job/a.md", false},
		{"/home/joe", "/home", false}, {"/home/joe", "/etc/passwd", false},
	} {
		if got := underRoot(c.root, c.path); got != c.ok {
			t.Errorf("underRoot(%q, %q) = %v, want %v", c.root, c.path, got, c.ok)
		}
	}
}

// A link that loops, or points at a link that lands nowhere, names a path
// through itself, which never resolves.
func TestALoopNamesAPathThroughItself(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, dest := range map[string]string{"a": "b", "b": "a", "self": "self", "dangle": "nowhere", "via": "dangle"} {
		if err := os.Symlink(dest, filepath.Join(root, name)); err != nil {
			t.Skip("symlinks unsupported")
		}
	}
	got, err := readDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range got {
		want := filepath.Join(root, e.name, e.name)
		if e.name == "dangle" {
			want = filepath.Join(root, "nowhere")
		}
		if e.kind != diskLink || e.targetKind != diskLink || e.target != want {
			t.Errorf("%s = kind %v target %q (%v), want a link to nowhere at %q", e.name, e.kind, e.target, e.targetKind, want)
		}
	}
}
