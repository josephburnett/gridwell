// Package server is the HTTP layer of Gridwell. It exposes RPC endpoints under
// /rpc/<MethodName> with JSON request/response bodies, an SSE endpoint at
// /rpc/Subscribe for real-time events, and serves the static web/ directory
// at /. Session middleware resolves the auth cookie to a user id before each
// non-public RPC.
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

	"github.com/josephburnett/gridwell/internal/store"
)

// Config configures the server.
type Config struct {
	// StaticDir is the directory served at /. Empty disables static files.
	StaticDir string
	// SecureCookie sets the Secure flag on the session cookie. Set to false
	// for local development over plain HTTP.
	SecureCookie bool
}

// Server is the wired-up HTTP server. Construct with New and mount via
// Server.Handler() into an http.Server.
type Server struct {
	cfg          Config
	store        *store.Store
	mux          *http.ServeMux
	urlStreamer  urlStreamer
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

// Handler returns the HTTP handler for the server.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	// Public RPCs (no session required).
	s.mux.HandleFunc("/rpc/Login", s.public(s.login))
	s.mux.HandleFunc("/rpc/Logout", s.public(s.logout))

	// Authenticated RPCs.
	s.mux.HandleFunc("/rpc/Whoami", s.authed(s.whoami))
	s.mux.HandleFunc("/rpc/GetGrid", s.authed(s.getGrid))
	s.mux.HandleFunc("/rpc/GetBlob", s.authed(s.getBlob))
	s.mux.HandleFunc("/rpc/GetTilePreview", s.authed(s.getTilePreview))

	s.mux.HandleFunc("/rpc/CreateWell", s.authed(s.createWell))
	s.mux.HandleFunc("/rpc/CreateFile", s.authed(s.createFile))
	s.mux.HandleFunc("/rpc/MoveTile", s.authed(s.moveTile))
	s.mux.HandleFunc("/rpc/CloneTile", s.authed(s.cloneTile))
	s.mux.HandleFunc("/rpc/ResizeTile", s.authed(s.resizeTile))
	s.mux.HandleFunc("/rpc/SetTileViewport", s.authed(s.setTileViewport))
	s.mux.HandleFunc("/rpc/SetGridDefaultView", s.authed(s.setGridDefaultView))
	s.mux.HandleFunc("/rpc/DeleteTile", s.authed(s.deleteTile))
	s.mux.HandleFunc("/rpc/UpdateFileContent", s.authed(s.updateFileContent))
	s.mux.HandleFunc("/rpc/AscendAtRoot", s.authed(s.ascendAtRoot))
	s.mux.HandleFunc("/rpc/Subscribe", s.authedSSE(s.subscribe))
	// URLStream is a WebSocket (GET upgrade), authenticated via the
	// session cookie inside the handler.
	s.mux.HandleFunc("/rpc/URLStream", s.urlStream)

	if s.cfg.StaticDir != "" {
		s.mux.Handle("/", s.staticOrSPA(s.cfg.StaticDir))
	}
}

// staticOrSPA serves files from dir, falling back to index.html for any
// request that isn't an /rpc/* call and doesn't match an on-disk file.
// This lets the WASM client own URLs like "/3/4/5" — direct hits and
// reloads land on index.html, the client decodes window.location, and
// hydrates the view.
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

// userIDKey is the context key for the resolved user id. Unexported to
// prevent middleware boundary leaks.
type userIDKey struct{}

func userIDFrom(ctx context.Context) (int64, bool) {
	v, ok := ctx.Value(userIDKey{}).(int64)
	return v, ok
}

// public wraps a handler that needs no session.
func (s *Server) public(h func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h(w, r)
	}
}

// authed wraps a handler that requires a valid session cookie. It resolves
// the cookie to a user id and stashes it in the request context.
func (s *Server) authed(h func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		uid, ok := s.resolveSession(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), userIDKey{}, uid)
		h(w, r.WithContext(ctx))
	}
}

// authedSSE is like authed but accepts GET (the EventSource fetch).
func (s *Server) authedSSE(h func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		uid, ok := s.resolveSession(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), userIDKey{}, uid)
		h(w, r.WithContext(ctx))
	}
}

// resolveSession returns the user id from the request's session cookie, or
// (0, false) if no valid session is present.
func (s *Server) resolveSession(r *http.Request) (int64, bool) {
	c, err := r.Cookie(SessionCookieName)
	if err != nil || c.Value == "" {
		return 0, false
	}
	uid, err := s.store.LookupSession(r.Context(), c.Value)
	if err != nil {
		return 0, false
	}
	return uid, true
}

// SessionCookieName is the cookie name used for sessions.
const SessionCookieName = "gridwell_session"

// readJSON decodes the request body into out. Returns a user-friendly error
// on malformed input.
func readJSON(r *http.Request, out any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decode body: %w", err)
	}
	return nil
}

// writeJSON encodes v as JSON to w with status 200. On encoder failure the
// connection is dropped without further headers; the caller has already
// committed status by writing.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// errorStatus maps a store sentinel error to an HTTP status code. The
// mapping is centralized here so handlers don't need to repeat the switch.
func errorStatus(err error) int {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, store.ErrPermissionDenied):
		return http.StatusForbidden
	case errors.Is(err, store.ErrInvalidArgument),
		errors.Is(err, store.ErrUnsupportedMime),
		errors.Is(err, store.ErrInvalidPath):
		return http.StatusBadRequest
	case errors.Is(err, store.ErrOverlap),
		errors.Is(err, store.ErrLocality):
		return http.StatusConflict
	case errors.Is(err, store.ErrNotURLTile):
		return http.StatusBadRequest
	case errors.Is(err, store.ErrChromiumUnavailable):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// writeError replies with a JSON error body and the appropriate status.
func writeError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(errorStatus(err))
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// uidOrError extracts the user id; on failure writes an unauthorized error.
// (Should never fail in practice — authed middleware guarantees presence —
// but the type assertion would panic without this.)
func uidOrError(w http.ResponseWriter, r *http.Request) (int64, bool) {
	uid, ok := userIDFrom(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return 0, false
	}
	return uid, true
}

// trimRPC strips the "/rpc/" prefix from an URL path; used in error logging.
func trimRPC(p string) string { return strings.TrimPrefix(p, "/rpc/") }

// silence unused.
var _ = trimRPC
