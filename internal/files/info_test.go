package files

import (
	"context"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A listing reads names and stats, never a file's bytes: a directory of
// files that render as nothing opens none of them, and what their previews need
// is on the rows.
func TestAListingOpensNoFile(t *testing.T) {
	f := newFixture(t)
	bin := f.write("firmware.bin", strings.Repeat("\x00", 1500))
	f.write("archive.tar.gz", "\x1f\x8b")
	f.write("notes.md", "# notes")
	var mu sync.Mutex
	opens := 0
	was := open
	t.Cleanup(func() { open = was })
	open = func(path string) (*opened, error) {
		mu.Lock()
		opens++
		mu.Unlock()
		return was(path)
	}
	tl := tileIn(t, f.grid(f.root), bin)
	mu.Lock()
	defer mu.Unlock()
	if opens != 0 {
		t.Errorf("a listing opened %d files, want none", opens)
	}
	if tl.TextPresentation != rpc.TextPresentationInfo || !strings.HasSuffix(tl.ContentStamp, "-1500") {
		t.Errorf("firmware.bin = presentation %q stamp %q, want info and its size on the stamp", tl.TextPresentation, tl.ContentStamp)
	}
}

func readInfo(t *testing.T, s *Source, id string) (*pb.FileInfo, []byte) {
	t.Helper()
	var info *pb.FileInfo
	var data []byte
	chunks := 0
	if err := s.ReadContent(context.Background(), &pb.ReadContentRequest{TileId: id, Info: true}, func(c *pb.ContentChunk) error {
		chunks++
		info, data = c.FileInfo, append(data, c.Data...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if chunks != 1 {
		t.Errorf("an info read sent %d chunks, want one", chunks)
	}
	return info, data
}

// An info read answers the inode's facts and the sniffed type in one chunk
// and no bytes, for a file that renders as nothing and a text file alike.
func TestAnInfoReadIsTheFilesFactsAndNoBytes(t *testing.T) {
	f := newFixture(t)
	bin := f.write("blob.bin", "\x7fELF\x02\x01\x01")
	if err := os.Chmod(bin, os.ModeSetuid|0o755); err != nil {
		t.Fatal(err)
	}
	info, data := readInfo(t, f.s, idOf(bin))
	if len(data) != 0 {
		t.Errorf("an info read sent %d bytes", len(data))
	}
	if info.GetPath() != bin || info.GetSize() != 7 || info.GetMode() != "-rwsr-xr-x" ||
		info.GetSniffedType() != "ELF executable" || info.GetExtensionType() != "binary" || info.GetMtimeNs() == 0 {
		t.Errorf("info = %+v", info)
	}
	if strings.Contains("linux darwin", runtime.GOOS) && (info.GetPerm() != 0o4755 || info.GetUid() != uint32(os.Getuid()) ||
		info.GetNlink() != 1 || info.GetInode() == 0 || info.GetCtimeNs() == 0 || info.GetUser() == "") {
		t.Errorf("inode facts = %+v", info)
	}
	text, _ := readInfo(t, f.s, idOf(f.write("notes.md", "# notes\n")))
	if text.GetSniffedType() != "text/plain; charset=utf-8" || text.GetExtensionType() != "md" {
		t.Errorf("a text file's info = %+v", text)
	}
}

func TestSniff(t *testing.T) {
	tar := make([]byte, 512)
	copy(tar[257:], "ustar")
	for want, head := range map[string][]byte{
		"empty":                     nil,
		"gzip":                      []byte("\x1f\x8b\x08"),
		"zip":                       []byte("PK\x03\x04rest"),
		"tar":                       tar,
		"SQLite database":           []byte("SQLite format 3\x00"),
		"application/octet-stream":  {0, 1, 2, 3, 0xff},
		"text/plain; charset=utf-8": []byte("hello"),
	} {
		if got := sniff(head); got != want {
			t.Errorf("sniff(%q) = %q, want %q", head, got, want)
		}
	}
}

func TestLsMode(t *testing.T) {
	for want, m := range map[string]os.FileMode{
		"-rw-r--r--": 0o644,
		"drwxr-xr-x": os.ModeDir | 0o755,
		"-rwSr--r--": os.ModeSetuid | 0o644,
		"-rwxr-sr-x": os.ModeSetgid | 0o755,
		"drwxrwxrwt": os.ModeDir | os.ModeSticky | 0o777,
		"lrwxrwxrwx": os.ModeSymlink | 0o777,
	} {
		if got := lsMode(m); got != want {
			t.Errorf("lsMode(%v) = %q, want %q", m, got, want)
		}
	}
}

// A file that renders as nothing serves its bytes as octet-stream by range,
// the hex view's pages.
func TestAFilesBytesServeByRange(t *testing.T) {
	f := newFixture(t)
	bin := f.write("blob.bin", "0123456789")
	var data []byte
	var first *pb.ServeContentChunk
	if err := f.s.ServeContent(context.Background(), &pb.ServeContentRequest{TileId: idOf(bin), Offset: 4, Length: 3},
		func(c *pb.ServeContentChunk) error {
			if first == nil {
				first = c
			}
			data = append(data, c.Data...)
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	if string(data) != "456" || first.GetMediaType() != bytesMediaType || first.GetSize() != 10 {
		t.Errorf("range = %q as %q of %d", data, first.GetMediaType(), first.GetSize())
	}
}
