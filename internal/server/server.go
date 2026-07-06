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
	"context"
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
	// NodeID is this node's durable identity (server.yaml node_id). It
	// qualifies the node grid — the plugin-list landing page every client
	// anchors at and every remote mounter descends into. Empty disables the
	// node grid (some unit tests exercise raw plugin routing only).
	NodeID string
	// NodeStatePath, when set, is the file the node grid persists its own
	// viewport to (the landing page's pan/zoom), so it survives a server
	// restart — the landing page stays as you left it. Empty = in-memory
	// only (tests).
	NodeStatePath string
}

// Server is the wired-up HTTP server. It holds NO Gridwell state of its own —
// no *store.Store anywhere. Every operation, data plane and infrastructure
// alike (shell PTY tile metadata, the preview endpoint), is routed through the
// plugin registry; the root plugin is the localdb instance whose grid is the
// app root. Construct with New and mount via Server.Handler().
type Server struct {
	cfg       Config
	pluginReg *plugin.Registry

	mux *http.ServeMux

	// nodeClient serves the node grid (the plugin-list landing page) when
	// cfg.NodeID is set; nodeClose tears down its in-process listener.
	nodeClient pb.GridwellClient
	nodeClose  func()

	// infoCache memoizes each plugin's first successful Info handshake, keyed
	// by plugin uuid. Identity, roots, and capabilities are stable for a
	// plugin's lifetime, so repeat ListPlugins / Subscribe calls must not
	// re-handshake every plugin (a consistently slow remote made every
	// palette open pay pluginInfoTimeout). Failures are never cached — the
	// next call retries. Invalidated on nothing today: a uuid is never
	// re-registered with a different backing plugin within one server run.
	infoMu    sync.Mutex
	infoCache map[string]*pb.InfoResponse
}

// New constructs a Server that routes everything through reg. With a NodeID
// configured, the server also serves the NODE GRID — the plugin-list landing
// page — as an in-process provider addressed like any plugin
// ("<node_id>/0"); every operation is addressed by a qualified id.
func New(reg *plugin.Registry, cfg Config) *Server {
	srv := &Server{
		cfg:       cfg,
		pluginReg: reg,
		mux:       http.NewServeMux(),
		infoCache: map[string]*pb.InfoResponse{},
	}
	if cfg.NodeID != "" {
		ng := &nodeGrid{reg: reg, info: srv.pluginInfo, invalidate: srv.invalidateInfoCache, statePath: cfg.NodeStatePath}
		ng.loadView()
		client, closer, err := plugin.ServeInProcess(ng)
		if err != nil {
			// In-process serving can only fail on loopback-listen exhaustion;
			// a node without its landing page is not worth starting.
			panic("gridwell: node grid: " + err.Error())
		}
		srv.nodeClient = client
		srv.nodeClose = closer
	}
	srv.routes()
	return srv
}

// routeClient resolves a plugin uuid to its client: the node grid provider
// for the node's own uuid, else the registry. The ONE routing lookup — the
// Connect handler, the shell WS bridge, the session endpoint, and the preview
// endpoint all resolve through here so the node grid is addressable
// everywhere a plugin is.
func (s *Server) routeClient(uuid string) (pb.GridwellClient, bool) {
	if s.cfg.NodeID != "" && uuid == s.cfg.NodeID {
		return s.nodeClient, true
	}
	return s.pluginReg.Get(uuid)
}

// clientForID resolves the plugin that owns a qualified id, returning its
// client and the local (unprefixed) id. Used by the shell + preview
// infrastructure to address a tile in whichever plugin holds it.
func (s *Server) clientForID(id string) (client pb.GridwellClient, local string, ok bool) {
	uuid, local, split := splitPluginID(id)
	if !split {
		return nil, "", false
	}
	c, found := s.routeClient(uuid)
	if !found {
		return nil, "", false
	}
	return c, local, true
}

func (s *Server) Handler() http.Handler { return s.mux }

// pluginInfo returns uuid's Info handshake, serving repeat calls from the
// per-uuid cache. The live call is bounded by pluginInfoTimeout so a hung
// plugin degrades (error, not stall); only a successful handshake is cached.
// Concurrent misses may both call Info — harmless, the values are identical.
func (s *Server) pluginInfo(ctx context.Context, uuid string) (*pb.InfoResponse, error) {
	s.infoMu.Lock()
	info, ok := s.infoCache[uuid]
	s.infoMu.Unlock()
	if ok {
		return info, nil
	}
	c, found := s.routeClient(uuid)
	if !found {
		return nil, errors.New("no plugin " + uuid)
	}
	ictx, cancel := context.WithTimeout(ctx, pluginInfoTimeout)
	defer cancel()
	info, err := c.Info(ictx, &pb.InfoRequest{})
	if err != nil {
		return nil, err
	}
	s.infoMu.Lock()
	s.infoCache[uuid] = info
	s.infoMu.Unlock()
	return info, nil
}

// invalidateInfoCache drops the cached Info for uuid so the next call re-fetches
// it from the plugin. Called by SetRootView after updating the root viewport:
// root_view_* are part of Info but change on every portal ascent, so the
// cache entry must be dropped to reflect the new framing on the next ListPlugins
// (page refresh).
func (s *Server) invalidateInfoCache(uuid string) {
	s.infoMu.Lock()
	delete(s.infoCache, uuid)
	s.infoMu.Unlock()
}

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

	// Per-plugin Chromium session blob, for the Electron host to hydrate /
	// dehydrate a partition. Plain HTTP (GET/PUT) so the main process uses a
	// simple fetch; the handler routes to the owning plugin's GetSession /
	// PutSession streams.
	s.mux.HandleFunc("/session/", s.sessionBlob)

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
