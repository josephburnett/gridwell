package files

// What the node reads off disk: a directory's names, a file's kind of
// content, its stamps, its body and its picture. Nothing here holds a row
// or an id.

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/josephburnett/gridwell/api/rpc"
)

type diskKind int

const (
	diskFile diskKind = iota
	diskDir
	// diskLink is a symlink, listed as itself, never as what it lands on.
	diskLink
	// diskOther is a FIFO, a socket or a device: an open could block on it
	// or read it forever, so it is never opened.
	diskOther
)

// diskEntry is one name in a directory, from Lstat. target is where a link
// lands with every symlink followed, or where a link that lands nowhere
// points (brokenTarget); targetKind is diskLink for one that lands nowhere.
type diskEntry struct {
	name, path string
	kind       diskKind
	size       int64
	mtime      time.Time
	target     string
	targetKind diskKind
}

// readDir lists dir by name, dotfiles included; a test counts its calls.
var readDir = func(dir string) ([]diskEntry, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	infos, err := f.Readdir(-1)
	if err != nil {
		return nil, err
	}
	out := make([]diskEntry, 0, len(infos))
	for _, fi := range infos {
		out = append(out, entryFromInfo(dir, fi))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

// statEntry is readDir's answer for one path.
func statEntry(path string) (diskEntry, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return diskEntry{}, err
	}
	return entryFromInfo(filepath.Dir(path), fi), nil
}

func entryFromInfo(dir string, fi os.FileInfo) diskEntry {
	e := diskEntry{name: fi.Name(), path: filepath.Join(dir, fi.Name()), size: fi.Size(), mtime: fi.ModTime()}
	switch mode := fi.Mode(); {
	case mode&os.ModeSymlink != 0:
		e.kind = diskLink
		e.target, e.targetKind = linkTarget(e.path)
	case mode.IsDir():
		e.kind = diskDir
	case !mode.IsRegular():
		e.kind = diskOther
	}
	return e
}

func linkTarget(link string) (string, diskKind) {
	real, err := filepath.EvalSymlinks(link)
	if err != nil {
		return brokenTarget(link), diskLink
	}
	fi, err := os.Stat(real)
	switch {
	case err != nil:
		return brokenTarget(link), diskLink
	case fi.IsDir():
		return real, diskDir
	case !fi.Mode().IsRegular():
		return real, diskOther
	}
	return real, diskFile
}

// brokenTarget is the path a link that lands nowhere points at, one hop, with
// its directory resolved, so the link goes live when that path appears. When
// that path is itself a link the chain loops or dangles further on, and the
// answer is the path through the link itself, which never resolves.
func brokenTarget(link string) string {
	through := filepath.Join(link, filepath.Base(link))
	dest, err := os.Readlink(link)
	if err != nil {
		return through
	}
	if !filepath.IsAbs(dest) {
		dest = filepath.Join(filepath.Dir(link), dest)
	}
	if dir, err := filepath.EvalSymlinks(filepath.Dir(dest)); err == nil {
		dest = filepath.Join(dir, filepath.Base(dest))
	}
	if _, err := os.Lstat(dest); err == nil {
		return through
	}
	return filepath.Clean(dest)
}

// refused is an error saying the OS will not let this user at the path, a
// verdict that stands until someone changes the permission, where any other
// error is a disk that cannot answer right now.
func refused(err error) bool { return errors.Is(err, iofs.ErrPermission) }

// gone is an error saying the path is not there: it does not exist, or
// something on the way is a file. Any other error is a disk that cannot
// answer right now.
func gone(err error) bool {
	return errors.Is(err, iofs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// pathErr drops the operation and path a *PathError repeats, for a sentence
// that already names the path.
func pathErr(err error) error {
	var pe *iofs.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

// mediaTypes is the node's own table, not mime.TypeByExtension, whose answers
// vary with the host's mime.types and would make a file a page on one
// machine and a text on another. An svg is not here: it can carry a script,
// so it is a text tile.
var mediaTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".avif": "image/avif", ".bmp": "image/bmp", ".ico": "image/x-icon",
	".pdf": "application/pdf",
	".mp4": "video/mp4", ".webm": "video/webm",
	".mp3": "audio/mpeg", ".ogg": "audio/ogg", ".wav": "audio/wav", ".m4a": "audio/mp4", ".flac": "audio/flac",
}

// mediaType is a page file's media type by its name, "" for every other file.
func mediaType(name string) string {
	return mediaTypes[strings.ToLower(filepath.Ext(name))]
}

// isPage is a page file: an image, video, audio or PDF, none of which runs a
// script, so the node serves it whole as a url tile's page.
func isPage(name string) bool { return mediaType(name) != "" }

// plainTextExts is a list rather than a sniff because a presentation is
// stamped on every row of a listing, which must not read the files.
var plainTextExts = map[string]bool{
	".txt": true, ".log": true, ".csv": true, ".tsv": true, ".json": true,
	".yaml": true, ".yml": true, ".toml": true, ".ini": true, ".cfg": true,
	".conf": true, ".env": true, ".xml": true, ".sql": true, ".proto": true,
	".go": true, ".py": true, ".js": true, ".ts": true, ".tsx": true,
	".jsx": true, ".c": true, ".h": true, ".cpp": true, ".hpp": true,
	".cc": true, ".rs": true, ".java": true, ".rb": true, ".sh": true,
	".bash": true, ".zsh": true, ".fish": true, ".pl": true, ".lua": true,
	".css": true, ".scss": true, ".dart": true, ".kt": true, ".swift": true,
	".diff": true, ".patch": true, ".lock": true, ".mod": true, ".sum": true,
	".service": true, ".gitignore": true, ".dockerignore": true,
	".html": true, ".htm": true, ".svg": true,
}

var plainTextNames = map[string]bool{
	"makefile": true, "dockerfile": true, "license": true, "readme": true,
	"changelog": true, "authors": true, "notice": true, "todo": true,
	"vagrantfile": true, "gemfile": true, "rakefile": true, "procfile": true,
	".gitignore": true, ".dockerignore": true, ".gitattributes": true,
	".editorconfig": true, ".profile": true, ".bashrc": true, ".zshrc": true,
}

// renderable is a document the client's renderer draws: markdown and org.
func renderable(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return strings.HasSuffix(n, ".md") || strings.HasSuffix(n, ".markdown") || strings.HasSuffix(n, ".org")
}

func isPlainText(name string) bool {
	lower := strings.ToLower(name)
	return plainTextExts[filepath.Ext(lower)] || plainTextNames[lower]
}

// presentation is a text file's, read off its name and never its bytes, so
// a listing opens nothing: a document renders, the plain families show
// verbatim, and any other name renders as nothing, so the file shows its
// file information. It is the one owner of what renders as text.
func presentation(name string) string {
	switch {
	case renderable(name):
		return rpc.TextPresentationBoth
	case isPlainText(name):
		return rpc.TextPresentationPlain
	}
	return rpc.TextPresentationInfo
}

// previewStamp is an image's mtime to the nanosecond, so two saves within a
// second are two pictures, and 0 for any other file.
func previewStamp(path string) int64 {
	if !strings.HasPrefix(mediaType(path), "image/") {
		return 0
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.ModTime().UnixNano()
}

// contentStamp is a file's rpc.ContentStamp.
func contentStamp(mtime time.Time, size int64) string {
	return rpc.ContentStamp(mtime.UnixNano(), size)
}

// bodyCap bounds a body: a document view, not a file transfer. A file past
// it shows its beginning, and is read-only.
const bodyCap = 4 << 20

// body is the text file f's body in the media type its presentation names:
// its bytes, up to the cap, and none for a file that shows its file
// information.
func body(f *os.File) (data []byte, media string, err error) {
	switch presentation(filepath.Base(f.Name())) {
	case rpc.TextPresentationInfo:
		return nil, "", nil
	case rpc.TextPresentationPlain:
		media = "text/plain"
	default:
		media = "text/markdown"
	}
	data, err = io.ReadAll(io.LimitReader(f, bodyCap))
	return data, media, err
}

// unwritable says why a file's body cannot be saved back to it: only a body
// that is the file's own bytes, whole, writes back. It is body's rule read
// the other way.
func unwritable(name string, size int64) error {
	switch {
	case isPage(name):
		return errors.New("is a page, which has no body to edit")
	case presentation(name) == rpc.TextPresentationInfo:
		return errors.New("is shown as its file information, which takes no edit")
	case size > bodyCap:
		return fmt.Errorf("is larger than the %d MiB a body shows, so its bytes are not all here to save", bodyCap>>20)
	}
	return nil
}

// denied says why this process may not replace the file at path. A save
// renames a new file in beside it, so the directory's mode binds too.
func denied(path string) error {
	if err := writeAccess(path); err != nil {
		return err
	}
	if err := writeAccess(filepath.Dir(path)); err != nil {
		return fmt.Errorf("its directory takes no new files: %w", err)
	}
	return nil
}

// readOnly is Tile.read_only for the text file at path: unwritable's rule,
// then denied's, "" when a write would take.
func readOnly(path string, size int64) string {
	name := filepath.Base(path)
	if err := unwritable(name, size); err != nil {
		return name + " " + err.Error()
	}
	if err := denied(path); err != nil {
		return name + " cannot be written: " + err.Error()
	}
	return ""
}

// underRoot is path within root's subtree, root included. filepath.Rel, not a
// root+"/" prefix, which for root "/" is "//" and matches nothing.
func underRoot(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

const (
	// previewEdge bounds a picture's longest edge: the whole image is one
	// descent away.
	previewEdge = 512
	// previewFileCap skips a file too large to decode on every paint; past
	// it the tile shows its name.
	previewFileCap = 64 << 20
	// previewPixelCap bounds what a decode allocates, up to eight bytes a
	// pixel (a 16-bit PNG), since a small file may declare any size: a
	// 16-megapixel photo has a picture, and past it the tile shows its name.
	previewPixelCap = 16_000_000
	// previewDecodes is how many pictures decode at once, so a directory of
	// large images asked for together holds what two decodes allocate, not all.
	previewDecodes = 2
)

// decodes holds a slot for each decode under way.
var decodes = make(chan struct{}, previewDecodes)

// previewJPEG is the image file f's picture, nil for any file that is not an
// image, is too large, or does not decode.
func previewJPEG(f *os.File, fi os.FileInfo) []byte {
	if !strings.HasPrefix(mediaType(f.Name()), "image/") || fi.Size() > previewFileCap {
		return nil
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	if c, _, err := image.DecodeConfig(bytes.NewReader(data)); err != nil || int64(c.Width)*int64(c.Height) > previewPixelCap {
		return nil
	}
	decodes <- struct{}{}
	img, _, err := image.Decode(bytes.NewReader(data))
	<-decodes
	if err != nil {
		return nil
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, downscale(img, previewEdge), &jpeg.Options{Quality: 80}); err != nil {
		return nil
	}
	return out.Bytes()
}

// downscale samples nearest-neighbour: a picture, not an archival resize.
func downscale(img image.Image, edge int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= edge && h <= edge {
		return img
	}
	scale := float64(edge) / float64(max(w, h))
	nw, nh := max(1, int(float64(w)*scale)), max(1, int(float64(h)*scale))
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := range nh {
		sy := b.Min.Y + y*h/nh
		for x := range nw {
			dst.Set(x, y, img.At(b.Min.X+x*w/nw, sy))
		}
	}
	return dst
}
