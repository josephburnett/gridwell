// Package server is the HTTP layer of Gridwell. It exposes RPC endpoints under
// /rpc/<MethodName> with JSON request/response bodies, an SSE endpoint at
// /rpc/Subscribe for real-time events, and serves the static web/ directory
// at /.
//
// Single-tenant: no auth, no sessions, no cookies. Callers should bind the
// listener to loopback only.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/josephburnett/gridwell/internal/rpc"
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
	st := s.store
	s.mux.HandleFunc("/rpc/Bootstrap", s.post(s.bootstrap))
	s.mux.HandleFunc("/rpc/GetGrid", s.post(handleJSONIn(func(ctx context.Context, req *rpc.GetGridRequest) (*rpc.GetGridResponse, error) {
		return st.GetGrid(ctx, req.GridID)
	})))
	s.mux.HandleFunc("/rpc/GetBlob", s.post(handleJSONIn(func(ctx context.Context, req *rpc.GetBlobRequest) (*rpc.GetBlobResponse, error) {
		data, err := st.GetBlob(ctx, req.BlobID)
		if err != nil {
			return nil, err
		}
		return &rpc.GetBlobResponse{Data: data}, nil
	})))
	s.mux.HandleFunc("/rpc/GetTilePreview", s.post(handleJSONIn(func(ctx context.Context, req *rpc.GetTilePreviewRequest) (*rpc.GetTilePreviewResponse, error) {
		jpeg, err := st.GetTilePreview(ctx, req.TileID)
		if err != nil {
			return nil, err
		}
		return &rpc.GetTilePreviewResponse{JPEG: jpeg}, nil
	})))

	s.mux.HandleFunc("/rpc/CreateWell", s.post(handleTile(st.CreateWell)))
	s.mux.HandleFunc("/rpc/CreateText", s.post(handleTile(st.CreateText)))
	s.mux.HandleFunc("/rpc/CreateURL", s.post(handleTile(st.CreateURL)))
	s.mux.HandleFunc("/rpc/CreateBlackHole", s.post(handleTile(st.CreateBlackHole)))
	s.mux.HandleFunc("/rpc/CreateFileWell", s.post(handleTile(st.CreateFileWell)))
	s.mux.HandleFunc("/rpc/CreateProcessWell", s.post(handleTile(st.CreateProcessWell)))
	s.mux.HandleFunc("/rpc/MoveTile", s.post(handleTile(st.MoveTile)))
	s.mux.HandleFunc("/rpc/CloneTile", s.post(handleTile(st.CloneTile)))
	s.mux.HandleFunc("/rpc/ResizeTile", s.post(handleTile(st.ResizeTile)))
	s.mux.HandleFunc("/rpc/SetWellView", s.post(handleTile(st.SetWellView)))
	s.mux.HandleFunc("/rpc/SetTextView", s.post(handleTile(st.SetTextView)))
	s.mux.HandleFunc("/rpc/SetRootView", s.post(handleVoid(st.SetRootView, rpc.SetRootViewResponse{})))
	s.mux.HandleFunc("/rpc/UpdateText", s.post(handleTile(st.UpdateText)))
	s.mux.HandleFunc("/rpc/DeleteTile", s.post(handleVoid(st.DeleteTile, rpc.DeleteTileResponse{})))
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

// handleTile is the canonical JSON-RPC handler for the store methods
// shaped (context.Context, *REQ) → (*rpc.Tile, error). The pattern
// — read req, dispatch, wrap the returned tile in rpc.TileResponse —
// was open-coded across every Create / Move / Clone / Resize / Set /
// Update handler. One generic helper here folds those into a single
// expression each at the routes table.
func handleTile[REQ any](action func(context.Context, *REQ) (*rpc.Tile, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req REQ
		if err := readJSON(r, &req); err != nil {
			writeError(w, err)
			return
		}
		n, err := action(r.Context(), &req)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, &rpc.TileResponse{Tile: *n})
	}
}

// handleVoid is the canonical handler for store methods shaped
// (context.Context, *REQ) → error. The response is the caller-supplied
// empty struct value (SetRootViewResponse, DeleteTileResponse), so
// the wire format stays unchanged.
func handleVoid[REQ, RES any](action func(context.Context, *REQ) error, empty RES) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req REQ
		if err := readJSON(r, &req); err != nil {
			writeError(w, err)
			return
		}
		if err := action(r.Context(), &req); err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, &empty)
	}
}

// handleJSONIn is the canonical handler for store methods shaped
// (context.Context, *REQ) → (RES, error) where RES is the JSON
// response value. The wrap step is the caller's job — used for
// getGrid / getBlob / getTilePreview where the response isn't
// uniformly *rpc.Tile.
func handleJSONIn[REQ, RES any](action func(context.Context, *REQ) (RES, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req REQ
		if err := readJSON(r, &req); err != nil {
			writeError(w, err)
			return
		}
		res, err := action(r.Context(), &req)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, res)
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
