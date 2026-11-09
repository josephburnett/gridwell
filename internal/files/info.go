package files

// A file's file information: what its inode says, read when the user opens
// it and never by a listing, and its type sniffed from its first bytes.

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// bytesMediaType is what a file that is not a page is served as: bytes no
// browser runs or renders.
const bytesMediaType = "application/octet-stream"

// readInfo sends the one chunk of an info read: the file's FileInfo. A name
// that is no regular file is never opened, and answers none.
func (s *Source) readInfo(id string, send func(*pb.ContentChunk) error) error {
	path, _ := pathOf(id)
	o, err := open(path)
	switch {
	case errors.Is(err, errIrregular):
		return send(&pb.ContentChunk{})
	case err != nil:
		return readRefusal(path, err)
	}
	defer o.Close()
	head := make([]byte, 512)
	n, err := io.ReadFull(o.File, head)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return diskRefusal("read", path, err)
	}
	info := &pb.FileInfo{
		Path: path, Size: o.fi.Size(), MtimeNs: o.fi.ModTime().UnixNano(),
		Mode:          lsMode(o.fi.Mode()),
		SniffedType:   sniff(head[:n]),
		ExtensionType: rpc.ExtensionType(filepath.Base(path)),
	}
	inode(o.File, o.fi, info)
	if u, err := user.LookupId(strconv.FormatUint(uint64(info.Uid), 10)); err == nil {
		info.User = u.Username
	}
	if g, err := user.LookupGroupId(strconv.FormatUint(uint64(info.Gid), 10)); err == nil {
		info.Group = g.Name
	}
	return send(&pb.ContentChunk{FileInfo: info, ContentStamp: contentStamp(o.fi.ModTime(), o.fi.Size())})
}

// lsMode spells a mode as ls -l does: the type, then each class's rwx with
// the setuid, setgid and sticky bits folded in as s and t.
func lsMode(m os.FileMode) string {
	b := []byte("----------")
	switch {
	case m.IsDir():
		b[0] = 'd'
	case m&os.ModeSymlink != 0:
		b[0] = 'l'
	case m&os.ModeNamedPipe != 0:
		b[0] = 'p'
	case m&os.ModeSocket != 0:
		b[0] = 's'
	case m&os.ModeCharDevice != 0:
		b[0] = 'c'
	case m&os.ModeDevice != 0:
		b[0] = 'b'
	}
	for i, c := range "rwxrwxrwx" {
		if m&(1<<uint(8-i)) != 0 {
			b[1+i] = byte(c)
		}
	}
	for _, sp := range []struct {
		bit     os.FileMode
		at      int
		exec, x byte
	}{{os.ModeSetuid, 3, 's', 'S'}, {os.ModeSetgid, 6, 's', 'S'}, {os.ModeSticky, 9, 't', 'T'}} {
		switch {
		case m&sp.bit == 0:
		case b[sp.at] == 'x':
			b[sp.at] = sp.exec
		default:
			b[sp.at] = sp.x
		}
	}
	return string(b)
}

// magic is the types a file's first bytes name, checked in order before
// http.DetectContentType, whose table knows no archive but zip and gzip and
// no executable.
var magic = []struct {
	at        int
	sig, name string
}{
	{0, "\x7fELF", "ELF executable"},
	{0, "\xcf\xfa\xed\xfe", "Mach-O executable"},
	{0, "\xce\xfa\xed\xfe", "Mach-O executable"},
	{0, "\xca\xfe\xba\xbe", "Mach-O universal binary or Java class"},
	{0, "MZ", "Windows executable"},
	{0, "\x00asm", "WebAssembly"},
	{0, "\x1f\x8b", "gzip"},
	{0, "BZh", "bzip2"},
	{0, "\xfd7zXZ\x00", "xz"},
	{0, "\x28\xb5\x2f\xfd", "zstd"},
	{0, "7z\xbc\xaf\x27\x1c", "7-zip"},
	{0, "Rar!\x1a\x07", "rar"},
	{0, "PK\x03\x04", "zip"},
	{257, "ustar", "tar"},
	{0, "SQLite format 3\x00", "SQLite database"},
	{0, "!<arch>\n", "ar archive"},
	{0, "-----BEGIN ", "PEM"},
	{0, "%PDF-", "PDF"},
	{0, "OggS", "Ogg"},
	{0, "fLaC", "FLAC"},
	{0, "ID3", "MP3"},
}

// sniff names what head, a file's first bytes, says it is: a magic row, or
// else http.DetectContentType's media type.
func sniff(head []byte) string {
	if len(head) == 0 {
		return "empty"
	}
	for _, m := range magic {
		if len(head) >= m.at+len(m.sig) && bytes.Equal(head[m.at:m.at+len(m.sig)], []byte(m.sig)) {
			return m.name
		}
	}
	return http.DetectContentType(head)
}
