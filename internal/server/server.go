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

	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/store"
)

// Config configures the server.
type Config struct {
	// StaticDir is the directory served at /. Empty disables static files.
	StaticDir string
}

// Server is the wired-up HTTP server. It holds NO Gridwell state of its own —
// no *store.Store anywhere. Every operation, data plane and infrastructure
// alike (shell PTY tile metadata, the preview endpoint), is routed through the
// plugin registry; the root plugin is the localdb instance whose grid is the
// app root. Construct with New and mount via Server.Handler().
type Server struct {
	cfg         Config
	pluginReg   *plugin.Registry
	primaryUUID string // the root localdb plugin (app root; target of id-less RPCs)

	mux           *http.ServeMux
	shellStreamer shellStreamer

	// activeShellSessions tracks the single live shell PTY per tile_id.
	// Same takeover semantics as URL (a refresh from another pane
	// evicts the previous holder).
	activeShellMu       sync.Mutex
	activeShellSessions map[string]*shellSessionEntry
}

// New constructs a Server that routes everything through reg. primaryUUID names
// the root localdb plugin (used for id-less RPCs and for the shell/preview
// infrastructure, which addresses the root plugin by tile id).
func New(reg *plugin.Registry, primaryUUID string, cfg Config) *Server {
	srv := &Server{
		cfg:         cfg,
		pluginReg:   reg,
		primaryUUID: primaryUUID,
		mux:         http.NewServeMux(),
	}
	srv.routes()
	return srv
}

// rootClient returns the gRPC client for the root plugin.
func (s *Server) rootClient() (pb.GridwellClient, bool) {
	return s.pluginReg.Get(s.primaryUUID)
}

// clientForID resolves the plugin that owns a qualified id, returning its
// client and the local (unprefixed) id. Used by the shell + preview
// infrastructure to address a tile in whichever plugin holds it.
func (s *Server) clientForID(id string) (client pb.GridwellClient, local string, ok bool) {
	uuid, local, split := splitPluginID(id)
	if !split {
		return nil, "", false
	}
	c, found := s.pluginReg.Get(uuid)
	if !found {
		return nil, "", false
	}
	return c, local, true
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

// writeHTTPError maps an error to the right HTTP status and writes a plain-text
// body. Used by the non-Connect endpoints (preview image, ShellStream). Errors
// now arrive from plugins as gRPC status errors, so map those codes; a raw
// store sentinel (should not occur post-routing) falls through to the same
// classifyStoreError categorization.
func writeHTTPError(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case gcodes.NotFound:
			code = http.StatusNotFound
		case gcodes.InvalidArgument:
			code = http.StatusBadRequest
		case gcodes.FailedPrecondition:
			code = http.StatusConflict
		}
		http.Error(w, st.Message(), code)
		return
	}
	switch classifyStoreError(err) {
	case classNotFound:
		code = http.StatusNotFound
	case classInvalidArgument:
		code = http.StatusBadRequest
	case classConflict:
		code = http.StatusConflict
	}
	http.Error(w, err.Error(), code)
}
