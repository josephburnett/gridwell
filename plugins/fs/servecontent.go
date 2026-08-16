package fs

// The fs plugin's web-content side (2026-08-11): any file tile can be
// fetched raw through the /content/ door, and files a browser can present
// natively — images, HTML, audio, video — additionally declare serves_page,
// so the client gives their descent url-tile semantics (a live view of the
// image, not a metadata summary). ReadContent is untouched: it remains the
// small DOCUMENT body (rendered markdown or the metadata summary); this
// door is the file itself.
//
// Subpath resolution makes a served HTML page whole: "img/cat.png" resolves
// against the page file's own directory, exactly like a web server's
// document-relative URL, confined to that directory's subtree. The HTTP
// door already refuses ".." at the URL grammar; the prefix check here is
// the plugin's own guarantee, so no future caller can walk out either.

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/doctype"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// serveChunkBytes mirrors localdb's read-side chunking.
const serveChunkBytes = 256 * 1024

// pageMediaTypes is the fs plugin's own table — deliberately not
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

// servesPage marks which of the served types are worth a PAGE DESCENT:
// what a browser presents natively as a whole document. Subresource types
// (css, js, json, txt) serve through the door for pages that reference
// them but keep their text-document descent themselves.
func servesPage(name string) bool {
	mt := pageMediaType(name)
	switch strings.SplitN(mt, "/", 2)[0] {
	case "image", "video", "audio":
		return true
	}
	return strings.HasPrefix(mt, "text/html") || mt == "application/pdf"
}

func pageMediaType(name string) string {
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

// textPresentation classifies a file tile's text-body presentation
// (decision 2026-08-13): markdown/org render (with the raw-source
// toggle); the plain-text families show verbatim; everything else has no
// declaration — the metadata summary renders as it always has.
func textPresentation(name string) string {
	if doctype.Renderable(name) {
		return rpc.TextPresentationBoth
	}
	lower := strings.ToLower(name)
	if plainTextExts[filepath.Ext(lower)] || plainTextNames[lower] {
		return rpc.TextPresentationPlain
	}
	return ""
}

// isPlainText reports the plain-text classification (the same rule
// textPresentation applies, minus the renderable arm).
func isPlainText(name string) bool {
	lower := strings.ToLower(name)
	return plainTextExts[filepath.Ext(lower)] || plainTextNames[lower]
}

// stampServesPage derives the serves_page bit onto loaded tile rows — from
// the one fact that owns it, the filename (AltText carries it; fs's label
// column). Derived at read time, never stored. dirPath, when non-empty,
// lets IMAGE tiles carry a preview GENERATION in preview_blob_id (the
// file's mtime): the client keys its thumbnail cache by that field, so an
// edited image invalidates naturally instead of showing last session's
// face forever.
func stampServesPage(tiles []*gridwellv1.Tile, dirPath string) {
	for _, t := range tiles {
		if t.Kind != "text" {
			continue
		}
		t.ServesPage = servesPage(t.AltText)
		t.TextPresentation = textPresentation(t.AltText)
		if dirPath != "" && strings.HasPrefix(pageMediaType(t.AltText), "image/") {
			if fi, err := os.Stat(filepath.Join(dirPath, t.AltText)); err == nil {
				t.PreviewBlobId = fi.ModTime().Unix()
			}
		}
	}
}

// ServeContent streams a file's raw bytes as web content. subpath "" is the
// tile's own file; a non-empty subpath is a page-relative resource resolved
// against the file's directory.
func (p *Plugin) ServeContent(req *gridwellv1.ServeContentRequest, stream grpc.ServerStreamingServer[gridwellv1.ServeContentChunk]) error {
	tileID, err := strconv.ParseInt(req.TileId, 10, 64)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "fs ServeContent: invalid tile_id %q", req.TileId)
	}
	var gridID int64
	var name, kind string
	err = p.db.QueryRow(`SELECT grid_id, name, kind FROM tiles WHERE id = ?`, tileID).Scan(&gridID, &name, &kind)
	if err != nil {
		return status.Errorf(codes.NotFound, "fs ServeContent: no tile %d", tileID)
	}
	if kind != "text" {
		return status.Error(codes.NotFound, "fs ServeContent: directories serve no page")
	}
	dirPath, err := p.gridPath(gridID)
	if err != nil {
		return err
	}
	target := filepath.Join(dirPath, name)
	if sub := req.Subpath; sub != "" {
		target = filepath.Join(dirPath, filepath.FromSlash(sub))
		// Confinement is the plugin's own invariant, independent of the
		// door's URL grammar: a resolved subpath never leaves the page's
		// directory subtree.
		if target != dirPath && !strings.HasPrefix(target, dirPath+string(filepath.Separator)) {
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
	if req.Subpath != "" {
		served = req.Subpath
	}
	mediaType := pageMediaType(served)
	buf := make([]byte, serveChunkBytes)
	n, readErr := io.ReadFull(f, buf)
	if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
		return readErr
	}
	if mediaType == "" {
		mediaType = sniffMediaType(buf[:n])
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

// sniffMediaType falls back to content sniffing for extensions outside the
// table — the door sets X-Content-Type-Options: nosniff, so the server-side
// sniff here is the only one that happens.
func sniffMediaType(head []byte) string {
	return http.DetectContentType(head)
}

func notFoundPage(stream grpc.ServerStreamingServer[gridwellv1.ServeContentChunk]) error {
	return stream.Send(&gridwellv1.ServeContentChunk{
		Status:    404,
		MediaType: "text/plain; charset=utf-8",
		Data:      []byte("not found"),
	})
}
