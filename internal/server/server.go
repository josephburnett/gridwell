// Package server is the HTTP layer of Gridwell. The RPC surface is
// served by a Connect-RPC handler at /gridwell.v1.Gridwell/<Method>
// (binary-proto and JSON-over-proto codecs both supported). The shell PTY
// is a raw-HTTP WebSocket at /rpc/ShellStream (Connect can't model it); the
// embed preview JPEG/PNG endpoint stays at /preview/tile/<id>; and the
// static web/ directory is served at /. Live URL tiles are hosted natively
// by the Electron shell (WebContentsView), so there is no URL WebSocket.
//
// Single-tenant: no auth, no sessions, no cookies. Callers should bind
// the listener to loopback only.
package server

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/store"
)

// Config configures the server.
type Config struct {
	// StaticDir is the directory served at /. Empty disables static files.
	StaticDir string
}

// Server is the wired-up HTTP server. Construct with New and mount via
// Server.Handler() into an http.Server.
type Server struct {
	cfg           Config
	store         *store.Store
	pluginReg     *plugin.Registry
	localdbUUID   string // UUID that identifies the local store as a plugin
	mux           *http.ServeMux
	shellStreamer shellStreamer

	// activeShellSessions tracks the single live shell PTY per tile_id.
	// Same takeover semantics as URL (a refresh from another pane
	// evicts the previous holder).
	activeShellMu       sync.Mutex
	activeShellSessions map[int64]*shellSessionEntry
}

// New constructs a Server bound to the given store.
func New(s *store.Store, cfg Config) *Server {
	srv := &Server{
		cfg:   cfg,
		store: s,
		mux:   http.NewServeMux(),
	}
	srv.routes()
	return srv
}

// SetPluginRegistry wires a plugin registry into the server. When set,
// GetGrid calls whose grid_id has a "<uuid>/" prefix are routed to the
// matching plugin instead of the local store.
func (s *Server) SetPluginRegistry(reg *plugin.Registry) {
	s.pluginReg = reg
}

// SetLocaldbUUID tells the server the UUID for the local store so that
// all IDs returned from the store are qualified as "<uuid>/<local>" on the
// wire, and incoming "<uuid>/<local>" IDs are stripped back to "<local>"
// before being passed to the store.
func (s *Server) SetLocaldbUUID(uuid string) {
	s.localdbUUID = uuid
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	// Connect-RPC handler covers the entire data plane. Subscribe is
	// the one server-streaming RPC; everything else is unary.
	path, handler := gridwellv1connect.NewGridwellHandler(newConnectHandler(s))
	s.mux.Handle(path, handler)

	// Shell PTY session is a WebSocket — Connect doesn't model it; raw
	// HTTP route stays. (Live URL tiles are hosted natively by the Electron
	// shell, so there is no URL WebSocket anymore.)
	s.mux.HandleFunc("/rpc/ShellStream", s.shellStream)

	// Embed preview is plain image bytes for external viewers (VS Code,
	// etc.); not RPC.
	s.mux.HandleFunc("/preview/tile/", s.previewTile)

	if s.cfg.StaticDir != "" {
		s.mux.Handle("/", s.staticOrSPA(s.cfg.StaticDir))
	}
}

// staticOrSPA serves files from dir, falling back to index.html for any
// request that isn't an /rpc/* call and doesn't match an on-disk file.
func (s *Server) staticOrSPA(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/rpc/") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/" {
			full := filepath.Join(dir, filepath.FromSlash(strings.TrimPrefix(r.URL.Path, "/")))
			if info, err := os.Stat(full); err == nil && !info.IsDir() {
				fs.ServeHTTP(w, r)
				return
			}
		}
		http.ServeFile(w, r, filepath.Join(dir, "index.html"))
	})
}

// storeErrorClass is the transport-neutral category of a store sentinel
// error. Both the Connect handler (asConnectError) and the raw-HTTP endpoints
// (writeHTTPError) map from this single classification, so the set of "which
// sentinels are invalid-argument vs conflict vs not-found" lives in exactly
// one place — a new sentinel can't be wired into one mapping and forgotten in
// the other.
type storeErrorClass int

const (
	classInternal storeErrorClass = iota
	classNotFound
	classInvalidArgument
	classConflict
)

// classifyStoreError categorizes a store sentinel error. nil maps to
// classInternal; callers handle nil before calling where it matters.
func classifyStoreError(err error) storeErrorClass {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return classNotFound
	case errors.Is(err, store.ErrInvalidArgument),
		errors.Is(err, store.ErrInvalidPath),
		errors.Is(err, store.ErrNotURLTile),
		errors.Is(err, store.ErrNotTextTile),
		errors.Is(err, store.ErrNotWellTile),
		errors.Is(err, store.ErrNotShellTile):
		return classInvalidArgument
	case errors.Is(err, store.ErrOverlap),
		errors.Is(err, store.ErrVersionConflict):
		return classConflict
	default:
		return classInternal
	}
}

// writeHTTPError maps a store sentinel error to the right HTTP status
// and writes a plain-text body. Used by the non-Connect endpoints
// (preview image, ShellStream) where Connect's code mapping doesn't
// apply.
func writeHTTPError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch classifyStoreError(err) {
	case classNotFound:
		status = http.StatusNotFound
	case classInvalidArgument:
		status = http.StatusBadRequest
	case classConflict:
		status = http.StatusConflict
	}
	http.Error(w, err.Error(), status)
}
