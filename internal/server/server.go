// Package server is the HTTP layer of Gridwell: a Connect-RPC codec at
// /gridwell.v1.Gridwell/<Method>, the static web client at /, and the shell
// WebSocket, so every host that runs the client has shells. Two doors,
// WebHandler and ConnectionHandler; nodeexport.go declares both.
package server

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/shellwire"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/plugin"
	"strconv"
)

// Config configures the server.
type Config struct {
	// StaticFS is the embedded web client, or an os.DirFS over a checkout when
	// server.yaml or --static overrides. Nil is a headless server.
	StaticFS fs.FS
	// ID is the node's own id: its home's namespace ("<id>/12") and the prefix
	// of every connection through it. Empty only in unit tests with no
	// connections.
	ID string
	// Password gates the browser mux (auth.go). New refuses an empty one, so
	// not even a test has an open path.
	Password string
	// DisableShells, from server.yaml, is the one server-side owner of the
	// node-wide shell refusal; Handshake carries it so the client drops the
	// shell primitive from the + palette.
	DisableShells bool
	// Home is the Gridwell home node.Options names; the trace door writes its
	// dumps under it. Empty refuses a dump rather than guessing a directory.
	Home string
}

// Server routes every operation through the registry by the first segment of
// its qualified id; home is where a client lands. Mount WebHandler and
// ConnectionHandler on their own listeners; see node.Start.
type Server struct {
	cfg       Config
	pluginReg *plugin.Registry

	mux *http.ServeMux

	// shellWriteTimeout is this server's PTY-output write bound; see
	// defaultShellWriteTimeout.
	shellWriteTimeout time.Duration

	// infoCache memoizes each plugin's first successful Info handshake by uuid,
	// because its facts are stable for the plugin's lifetime and without it a
	// slow remote makes every palette open pay pluginInfoTimeout. Failures are
	// never cached.
	infoMu    sync.Mutex
	infoCache map[string]*pb.InfoResponse
}

// New refuses an empty Config.Password: the browser door has no open mode.
func New(reg *plugin.Registry, cfg Config) (*Server, error) {
	if cfg.Password == "" {
		return nil, errors.New("server: a web password is required (the browser door is never open)")
	}
	srv := &Server{
		cfg:               cfg,
		pluginReg:         reg,
		mux:               http.NewServeMux(),
		shellWriteTimeout: defaultShellWriteTimeout,
		infoCache:         map[string]*pb.InfoResponse{},
	}
	srv.routes()
	return srv, nil
}

// routeClient resolves a namespace uuid to home or a plugin. A connection is
// not addressable by uuid alone; resolve is the lookup that sees it.
func (s *Server) routeClient(uuid string) (namespace.Namespace, bool) {
	return s.pluginReg.Get(uuid)
}

// resolve is the routing lookup every door goes through: the namespace owning a
// qualified id, the local id it understands, the uuid to re-qualify with, and
// whether it is transit. "<id>/<digits>" is the home store; "<id>/<letters>/…"
// is a connection, whose local id keeps the connection segment for the
// transport to peel; anything else is a plugin by uuid.
//
// The transport is the only transit namespace, structurally: a plugin's ids are
// its own, since the node mints them, while a connection's arrive already
// qualified from the far node's frame.
func (s *Server) resolve(id string) (ns namespace.Namespace, local, uuid string, transit, ok bool) {
	uuid, local, split := rpc.SplitID(id)
	if !split {
		return nil, "", "", false, false
	}
	// rpc.OwnerNamespaceOf is the peel, shared with every other reader of an
	// id, so the router and the address bar cannot disagree.
	if s.cfg.ID != "" && rpc.OwnerNamespaceOf(id, s.cfg.ID) != uuid {
		t, has := s.pluginReg.Transport()
		return t, local, uuid, true, has
	}
	c, found := s.pluginReg.Get(uuid)
	return c, local, uuid, false, found
}

// clientForID resolves the namespace that owns a qualified id, and the local
// id.
func (s *Server) clientForID(id string) (ns namespace.Namespace, local string, ok bool) {
	c, local, _, _, found := s.resolve(id)
	return c, local, found
}

// namespaceRow is one namespace this node declares. Kind is the registry's
// configured kind, empty for the transport, whose rows carry their own.
type namespaceRow struct {
	UUID, Kind string
	NS         namespace.Namespace
	Transit    bool
}

// namespaces enumerates what this node declares, in the one order every
// fan-out takes: the registered plugins as configured, then the transport
// under the node's own id, which is the id a connection's rows re-qualify
// through and the only transit namespace. A node without an id declares no
// connections.
func (s *Server) namespaces() []namespaceRow {
	var out []namespaceRow
	for _, p := range s.pluginReg.Ordered() {
		c, ok := s.routeClient(p.UUID)
		if !ok {
			continue
		}
		out = append(out, namespaceRow{UUID: p.UUID, Kind: p.Kind, NS: c})
	}
	if t, ok := s.pluginReg.Transport(); ok && s.cfg.ID != "" {
		out = append(out, namespaceRow{UUID: s.cfg.ID, NS: t, Transit: true})
	}
	return out
}

// homeUUID is the configured node id, or the first registered entry when a test
// wires a registry without one, since home is registered first.
func (s *Server) homeUUID() string {
	if s.cfg.ID != "" {
		return s.cfg.ID
	}
	if o := s.pluginReg.Ordered(); len(o) > 0 {
		return o[0].UUID
	}
	return ""
}

// pluginInfo serves repeat calls from the per-uuid cache. The live call is
// bounded by pluginInfoTimeout so a hung plugin degrades to an error rather
// than a stall; concurrent misses may both call Info, which is harmless.
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

// invalidateInfoCache drops the cached Info for uuid. The root_view_* fields
// are part of Info but change on every ascent out of a plugin root, so
// SetFraming's root arm calls it.
func (s *Server) invalidateInfoCache(uuid string) {
	s.infoMu.Lock()
	delete(s.infoCache, uuid)
	s.infoMu.Unlock()
}

func (s *Server) routes() {
	// A thin codec over the one in-process router.
	path, handler := gridwellv1connect.NewGridwellHandler(newConnectHandler(newRouter(s)),
		connect.WithInterceptors(traceInterceptor{}))
	s.mux.Handle(path, handler)

	// One live PTY per WebSocket, on the same gated mux (shell_door.go).
	s.mux.Handle(shellwire.Path, s.shellDoor())

	// Exempt from the cookie gate; see content_door.go.
	s.mux.Handle(contentPathPrefix, s.contentDoor())

	// The diagnostic ring, gated like everything else here (trace_door.go).
	s.mux.Handle(tracePath, s.traceDoor())
	s.mux.Handle(traceDumpPath, s.traceDumpDoor())

	if s.cfg.StaticFS != nil {
		s.mux.Handle("/", s.staticOrSPA(s.cfg.StaticFS))
	}
}

// staticOrSPA falls back to index.html for any request that matches no file,
// which is the client's path grammar. The /rpc/ prefix stays a hard 404 so a
// stale caller gets an error rather than HTML.
func (s *Server) staticOrSPA(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/rpc/") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/" {
			name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
			if info, err := fs.Stat(fsys, name); err == nil && !info.IsDir() {
				if serveGzipSidecar(w, r, fsys, name, info) {
					return
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		http.ServeFileFS(w, r, fsys, "index.html")
	})
}

// serveGzipSidecar serves <file>.gz when the client accepts gzip and the
// sidecar is at least as new as the raw file, so a stale sidecar never shadows
// the real bytes; embedded files share one zero modtime and are always
// same-aged. Content-Type comes from the raw file's extension, because
// instantiateStreaming requires application/wasm.
func serveGzipSidecar(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string, raw fs.FileInfo) bool {
	if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		return false
	}
	gzInfo, err := fs.Stat(fsys, name+".gz")
	if err != nil || gzInfo.IsDir() || gzInfo.ModTime().Before(raw.ModTime()) {
		return false
	}
	f, err := fsys.Open(name + ".gz")
	if err != nil {
		return false
	}
	defer f.Close()
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		return false // ServeContent needs seeking; embed.FS and os.DirFS provide it
	}
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("Vary", "Accept-Encoding")
	// With Content-Encoding the browser knows only the compressed length, so
	// hand over the raw size; index.html reads it for its loading percentage.
	w.Header().Set("X-Uncompressed-Size", strconv.FormatInt(raw.Size(), 10))
	http.ServeContent(w, r, path.Base(name), gzInfo.ModTime(), rs)
	return true
}

// The sentinel-to-class table lives in api/gwerr, next to the sentinel
// declarations, so every codec maps from the one classification.
