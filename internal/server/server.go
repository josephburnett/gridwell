// Package server is the HTTP layer of Gridwell. It exposes RPC endpoints under
// /rpc/<MethodName> with JSON request/response bodies, an SSE endpoint at
// /rpc/Subscribe for real-time events, and serves the static web/ directory
// at /.
//
// Single-tenant: no auth, no sessions, no cookies. Callers should bind the
// listener to loopback only.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

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
	cfg         Config
	store       *store.Store
	mux         *http.ServeMux
	urlStreamer urlStreamer

	// activeURLSessions tracks the single live URL session per tile_id.
	// Protected by activeURLMu.
	activeURLMu       sync.Mutex
	activeURLSessions map[int64]*urlSessionEntry
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

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	s.mux.HandleFunc("/rpc/Bootstrap", s.post(s.bootstrap))
	s.mux.HandleFunc("/rpc/GetGrid", s.post(s.getGrid))
	s.mux.HandleFunc("/rpc/GetBlob", s.post(s.getBlob))
	s.mux.HandleFunc("/rpc/GetTilePreview", s.post(s.getTilePreview))

	s.mux.HandleFunc("/rpc/CreateWell", s.post(s.createWell))
	s.mux.HandleFunc("/rpc/CreateText", s.post(s.createText))
	s.mux.HandleFunc("/rpc/CreateURL", s.post(s.createURL))
	s.mux.HandleFunc("/rpc/CreateBlackHole", s.post(s.createBlackHole))
	s.mux.HandleFunc("/rpc/MoveTile", s.post(s.moveTile))
	s.mux.HandleFunc("/rpc/CloneTile", s.post(s.cloneTile))
	s.mux.HandleFunc("/rpc/ResizeTile", s.post(s.resizeTile))
	s.mux.HandleFunc("/rpc/SetWellView", s.post(s.setWellView))
	s.mux.HandleFunc("/rpc/SetTextView", s.post(s.setTextView))
	s.mux.HandleFunc("/rpc/SetRootView", s.post(s.setRootView))
	s.mux.HandleFunc("/rpc/UpdateText", s.post(s.updateText))
	s.mux.HandleFunc("/rpc/DeleteTile", s.post(s.deleteTile))
	s.mux.HandleFunc("/rpc/Subscribe", s.get(s.subscribe))
	s.mux.HandleFunc("/rpc/URLStream", s.urlStream)

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

// post wraps a handler that requires POST.
func (s *Server) post(h func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h(w, r)
	}
}

// get wraps a handler that requires GET (used by the SSE stream).
func (s *Server) get(h func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h(w, r)
	}
}

func readJSON(r *http.Request, out any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decode body: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func errorStatus(err error) int {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, store.ErrInvalidArgument),
		errors.Is(err, store.ErrInvalidPath):
		return http.StatusBadRequest
	case errors.Is(err, store.ErrOverlap):
		return http.StatusConflict
	case errors.Is(err, store.ErrVersionConflict):
		return http.StatusConflict
	case errors.Is(err, store.ErrNotURLTile),
		errors.Is(err, store.ErrNotTextTile),
		errors.Is(err, store.ErrNotWellTile):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func writeError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(errorStatus(err))
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
