// Package fsfile is the PURE core of the fs projection: every derivation
// from a filename or file bytes, with no database and no tile ids. It is
// shared by the legacy fs plugin and the v2 fs content provider, so the
// two answer identically BY CONSTRUCTION — the migration parity gate
// (docs/v2-design.md §8.4) leans on that.
package fsfile

import (
	"bytes"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/doctype"
	"github.com/josephburnett/gridwell/plugins/fs/fssource"
)

// pageMediaTypes is fs's own table — deliberately not
// mime.TypeByExtension, whose answers vary with the host's mime.types and
// would make serves_page differ between machines. A file is a PAGE when a
// browser presents it natively; everything else still serves raw through
// the door (with a sniffed type) but keeps its document descent.
var pageMediaTypes = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".avif": "image/avif",
	".bmp":  "image/bmp",
	".ico":  "image/x-icon",
	".svg":  "image/svg+xml",
	".html": "text/html; charset=utf-8",
	".htm":  "text/html; charset=utf-8",
	".pdf":  "application/pdf",
	".mp4":  "video/mp4",
	".webm": "video/webm",
	".mp3":  "audio/mpeg",
	".ogg":  "audio/ogg",
	".wav":  "audio/wav",
	".m4a":  "audio/mp4",
	".flac": "audio/flac",
	".css":  "text/css; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".json": "application/json",
	".txt":  "text/plain; charset=utf-8",
}

// ServesPage marks which of the served types are worth a PAGE DESCENT:
// what a browser presents natively as a whole document. Subresource types
// (css, js, json, txt) serve through the door for pages that reference
// them but keep their text-document descent themselves.
func ServesPage(name string) bool {
	mt := PageMediaType(name)
	switch strings.SplitN(mt, "/", 2)[0] {
	case "image", "video", "audio":
		return true
	}
	return strings.HasPrefix(mt, "text/html") || mt == "application/pdf"
}

// PageMediaType returns the declared page media type for a filename, or
// "" for names outside the table.
func PageMediaType(name string) string {
	return pageMediaTypes[strings.ToLower(filepath.Ext(name))]
}

// plainTextExts marks extensions whose bodies present as PLAIN text
// (monospace, no markdown interpretation — source, config, logs, data).
// Deliberately a list, not a sniff: presentation stamps onto every tile
// row at grid load, which must never stat or read the files.
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
}

// plainTextNames covers the extensionless classics.
var plainTextNames = map[string]bool{
	"makefile": true, "dockerfile": true, "license": true, "readme": true,
	"changelog": true, "authors": true, "notice": true, "todo": true,
	"vagrantfile": true, "gemfile": true, "rakefile": true, "procfile": true,
	".gitignore": true, ".dockerignore": true, ".gitattributes": true,
	".editorconfig": true, ".profile": true, ".bashrc": true, ".zshrc": true,
}

// TextPresentation classifies a file tile's text-body presentation
// (decision 2026-08-13): markdown/org render (with the raw-source
// toggle); the plain-text families show verbatim; everything else has no
// declaration — the metadata summary renders as it always has.
func TextPresentation(name string) string {
	if doctype.Renderable(name) {
		return rpc.TextPresentationBoth
	}
	lower := strings.ToLower(name)
	if plainTextExts[filepath.Ext(lower)] || plainTextNames[lower] {
		return rpc.TextPresentationPlain
	}
	return ""
}

// IsPlainText reports the plain-text classification (TextPresentation's
// rule, minus the renderable arm).
func IsPlainText(name string) bool {
	lower := strings.ToLower(name)
	return plainTextExts[filepath.Ext(lower)] || plainTextNames[lower]
}

// PreviewStamp returns the cheap preview generation for a file — an
// image's mtime, 0 for everything else (Tile.preview_blob_id on the
// wire; the client's thumbnail-cache key).
func PreviewStamp(dirPath, name string) int64 {
	if dirPath == "" || !strings.HasPrefix(PageMediaType(name), "image/") {
		return 0
	}
	fi, err := os.Stat(filepath.Join(dirPath, name))
	if err != nil {
		return 0
	}
	return fi.ModTime().Unix()
}

// renderableBodyCap bounds how much of a renderable file the descent body
// carries (issue #236): a document view, not a file transfer. Files past
// the cap fall back to the metadata summary.
const renderableBodyCap = 4 << 20

// Body returns a file's descent body: real bytes for renderable/plain
// files under the cap, the metadata summary otherwise. A missing or
// unstattable file returns (nil, "") — the caller decides what absence
// means.
func Body(dirPath, name string) (data []byte, mediaType string) {
	fullPath := filepath.Join(dirPath, name)
	entry, err := fssource.Stat(fullPath)
	if err != nil {
		return nil, ""
	}
	if (doctype.Renderable(name) || IsPlainText(name)) && entry.Size <= renderableBodyCap {
		if body, readErr := os.ReadFile(fullPath); readErr == nil {
			if IsPlainText(name) && !doctype.Renderable(name) {
				return body, "text/plain"
			}
			return body, "text/markdown"
		}
		// Unreadable despite the stat: the metadata summary still tells
		// the user what is here instead of a blank pane (charter §6).
	}
	return []byte(fssource.MetadataMarkdown(entry)), "text/markdown"
}

// serveChunkBytes mirrors localdb's read-side chunking.
const serveChunkBytes = 256 * 1024

// ServeChunkSender is the streaming half both callers provide (the
// legacy plugin's grpc stream and the v2 adapter's — same chunk type).
type ServeChunkSender interface {
	Send(*gridwellv1.ServeContentChunk) error
}

// ServeFile streams a file's raw bytes as web content: subpath "" is the
// named file itself; a non-empty subpath is a page-relative resource
// resolved against the file's directory, CONFINED to that directory's
// subtree (the plugin-side guarantee, independent of the door's URL
// grammar). Absence answers a 404 page, never an error.
func ServeFile(stream ServeChunkSender, dirPath, name, subpath string) error {
	target := filepath.Join(dirPath, name)
	if subpath != "" {
		target = filepath.Join(dirPath, filepath.FromSlash(subpath))
		if !UnderRoot(dirPath, target) {
			return notFoundPage(stream)
		}
	}
	f, err := os.Open(target)
	if err != nil {
		return notFoundPage(stream)
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || fi.IsDir() {
		return notFoundPage(stream)
	}

	served := name
	if subpath != "" {
		served = subpath
	}
	mediaType := PageMediaType(served)
	buf := make([]byte, serveChunkBytes)
	n, readErr := io.ReadFull(f, buf)
	if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
		return readErr
	}
	if mediaType == "" {
		// The door sets X-Content-Type-Options: nosniff, so this
		// server-side sniff is the only one that happens.
		mediaType = http.DetectContentType(buf[:n])
	}
	if err := stream.Send(&gridwellv1.ServeContentChunk{Status: 200, MediaType: mediaType, Data: buf[:n]}); err != nil {
		return err
	}
	for readErr == nil {
		n, readErr = io.ReadFull(f, buf)
		if n > 0 {
			if err := stream.Send(&gridwellv1.ServeContentChunk{Data: buf[:n]}); err != nil {
				return err
			}
		}
	}
	if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
		return nil
	}
	return readErr
}

func notFoundPage(stream ServeChunkSender) error {
	return stream.Send(&gridwellv1.ServeContentChunk{
		Status:    404,
		MediaType: "text/plain; charset=utf-8",
		Data:      []byte("not found"),
	})
}

const (
	// previewMaxEdge bounds the thumbnail: previews are small tiles, and the
	// full image is always one descent away through the /content/ door.
	previewMaxEdge = 512
	// previewFileCap skips absurdly large files rather than decode them on
	// every grid paint. Past it the tile falls back to its label, like a url
	// tile with no capture yet.
	previewFileCap = 64 << 20
)

// PreviewJPEG returns a bounded JPEG thumbnail for an image file, or nil
// for non-images, oversized, and undecodable files — the caller treats
// nil as "no preview", never an error.
func PreviewJPEG(dirPath, name string) []byte {
	if !strings.HasPrefix(PageMediaType(name), "image/") {
		return nil
	}
	full := filepath.Join(dirPath, name)
	if fi, err := os.Stat(full); err != nil || fi.Size() > previewFileCap {
		return nil
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil
	}
	return thumbnailJPEG(data)
}

// thumbnailJPEG decodes any stdlib-supported image (png, jpeg, gif) and
// re-encodes a bounded JPEG. Undecodable input (webp, svg, a corrupt
// file) returns nil.
func thumbnailJPEG(data []byte) []byte {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	img = downscale(img, previewMaxEdge)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 80}); err != nil {
		return nil
	}
	return out.Bytes()
}

// downscale bounds the longest edge to maxEdge with nearest-neighbor
// sampling — a preview, not an archival resize; stdlib-only on purpose.
func downscale(img image.Image, maxEdge int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxEdge && h <= maxEdge {
		return img
	}
	scale := float64(maxEdge) / float64(w)
	if h > w {
		scale = float64(maxEdge) / float64(h)
	}
	nw, nh := int(float64(w)*scale), int(float64(h)*scale)
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		sy := b.Min.Y + y*h/nh
		for x := 0; x < nw; x++ {
			sx := b.Min.X + x*w/nw
			dst.Set(x, y, img.At(sx, sy))
		}
	}
	return dst
}
