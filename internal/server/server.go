// Package server is the HTTP layer of Gridwell. The RPC surface is served by
// a Connect-RPC codec at /gridwell.v1.Gridwell/<Method>, in both
// binary-proto and JSON-over-proto codecs, and the static web client is
// served at /. Live url tiles are hosted natively by the Electron shell;
// shell PTY bytes ride a WebSocket on this same gated door (shell_door.go),
// so every host that runs the client has shells.
//
// Two doors. WebHandler is the browser surface, always gated behind the
// password — the minted <home>/web-password file, and one cookie derived from
// it in auth.go — and bindable to a network. FederationHandler is the
// raw-gRPC node export, ungated and served only on the 0600 unix socket
// node.listenFederation opens.
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
	// StaticFS is the filesystem served at /: normally the embedded web
	// client, since the binary is self-contained, or an os.DirFS over a
	// checkout when server.yaml or --static overrides. Nil disables static
	// files, for a headless server.
	StaticFS fs.FS
	// ID is the node's own id: its home's namespace ("<id>/12") and the
	// prefix of every connection through it ("<id>/<conn>/…"). It is empty in
	// unit tests over raw plugin routing, which have no connections.
	ID string
	// Password gates the browser mux behind the login-page cookie; see
	// auth.go. It is required, and New refuses an empty one. The web door is
	// never open: BuildConfig mints the password on first serve, so
	// production has no open path, and this refusal keeps a test from having
	// one either.
	Password string
	// DisableShells refuses shell tiles node-wide: CreateTile(kind=shell) and
	// OpenShell are denied for every namespace, ShellSessionAlive answers
	// gone, and Handshake carries the flag so the client drops the shell
	// primitive from the + palette. This field, from server.yaml's
	// disable_shells, is the one server-side owner of the fact.
	DisableShells bool
}

// Server is the wired-up HTTP server: a router. Every operation is routed
// through the registry by the first segment of its qualified id, and home,
// the first configured entry with a root, is where a client lands. Construct
// it with New and mount WebHandler, the browser door, and FederationHandler,
// the node door, on their own listeners; see node.Start.
type Server struct {
	cfg       Config
	pluginReg *plugin.Registry

	mux *http.ServeMux

	// infoCache memoizes each plugin's first successful Info handshake, keyed
	// by uuid. Identity, roots, and capabilities are stable for a plugin's
	// lifetime, so repeat Handshake and Subscribe calls must not re-handshake
	// every plugin, or a consistently slow remote makes every palette open pay
	// pluginInfoTimeout. Failures are never cached and the next call retries.
	// invalidateInfoCache drops an entry when a plugin's declared facts change
	// under one uuid, as a root SetFraming write does.
	infoMu    sync.Mutex
	infoCache map[string]*pb.InfoResponse
}

// New constructs a Server that routes everything through reg; every operation
// is addressed by a qualified id. An empty Password is refused: the browser
// door has no open mode.
func New(reg *plugin.Registry, cfg Config) (*Server, error) {
	if cfg.Password == "" {
		return nil, errors.New("server: a web password is required (the browser door is never open)")
	}
	srv := &Server{
		cfg:       cfg,
		pluginReg: reg,
		mux:       http.NewServeMux(),
		infoCache: map[string]*pb.InfoResponse{},
	}
	srv.routes()
	return srv, nil
}

// routeClient resolves a namespace uuid to its Namespace: home or a plugin. A
// connection is not addressable by uuid alone — it lives under the node's id,
// "<id>/<conn>/…" — and resolve is the lookup that sees it.
func (s *Server) routeClient(uuid string) (namespace.Namespace, bool) {
	return s.pluginReg.Get(uuid)
}

// resolve is the routing lookup: the namespace that owns a qualified id, the
// local unprefixed id it understands, the uuid to re-qualify answers with, and
// whether the namespace is transit, meaning its ids are chains from another
// node and the transit qualification rule applies. "<id>/<digits>" is the home
// store; "<id>/<letters>/…" is a connection, owned by the transport, whose
// local id keeps the connection segment for the transport to peel; anything
// else is a plugin by uuid. The Connect codec, the federation codec, the
// content door, and the shell door all resolve through here.
//
// The transport is the only transit namespace, and that is structural rather
// than a declaration: a plugin's ids are its own, since the node mints them,
// while a connection's arrive already qualified from the far node's frame.
func (s *Server) resolve(id string) (ns namespace.Namespace, local, uuid string, transit, ok bool) {
	uuid, local, split := rpc.SplitID(id)
	if !split {
		return nil, "", "", false, false
	}
	if s.cfg.ID != "" && uuid == s.cfg.ID && !localIsHome(local) {
		t, has := s.pluginReg.Transport()
		return t, local, uuid, true, has
	}
	c, found := s.pluginReg.Get(uuid)
	return c, local, uuid, false, found
}

// localIsHome reports whether a local id under the node's own segment names
// the home store — its first segment is a TILE segment, a grid or tile id —
// rather than a connection, whose name is a namespace segment. The shape
// question is rpc.IsTileSegment's, the same classifier the URL grammar asks,
// so the router and the address bar can never disagree about which segments
// are a namespace chain.
func localIsHome(local string) bool {
	first := local
	if i := strings.IndexByte(local, '/'); i >= 0 {
		first = local[:i]
	}
	return first == "" || rpc.IsTileSegment(first)
}

// clientForID resolves the namespace that owns a qualified id, returning it
// and the local id. The shell and preview paths use it to address a tile
// wherever it lives.
func (s *Server) clientForID(id string) (ns namespace.Namespace, local string, ok bool) {
	c, local, _, _, found := s.resolve(id)
	return c, local, found
}

// pluginInfo returns uuid's Info handshake, serving repeat calls from the
// per-uuid cache. The live call is bounded by pluginInfoTimeout so a hung
// plugin degrades to an error rather than a stall, and only a successful
// handshake is cached. Concurrent misses may both call Info, which is
// harmless: the values are identical.
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

// invalidateInfoCache drops the cached Info for uuid so the next call
// re-fetches it. SetFraming's root arm calls it after updating the root
// viewport: the root_view_* fields are part of Info but change on every ascent
// out of a plugin root, so the entry must be dropped for the next Handshake to
// see the new framing.
func (s *Server) invalidateInfoCache(uuid string) {
	s.infoMu.Lock()
	delete(s.infoCache, uuid)
	s.infoMu.Unlock()
}

func (s *Server) routes() {
	// The Connect-RPC handler covers the browser's data plane: a thin codec
	// over the one in-process router, the same router the federation door
	// serves through the other codec.
	path, handler := gridwellv1connect.NewGridwellHandler(newConnectHandler(newRouter(s)))
	s.mux.Handle(path, handler)

	// The /shell door: one live PTY per WebSocket, on the same gated mux as
	// every other page request; see shell_door.go. Shells ride the web door,
	// so every host that runs the client has them.
	s.mux.Handle(shellwire.Path, s.shellDoor())

	// The /content/ door: plugin-served web content; see content_door.go. It
	// is registered on the mux but exempt from the cookie gate, because the
	// content token in the path is the credential there.
	s.mux.Handle(contentPathPrefix, s.contentDoor())

	if s.cfg.StaticFS != nil {
		s.mux.Handle("/", s.staticOrSPA(s.cfg.StaticFS))
	}
}

// staticOrSPA serves files from fsys — the embedded web client, or a checkout
// — falling back to index.html for any request that does not match a file,
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

// serveGzipSidecar serves <file>.gz with Content-Encoding: gzip when the
// client accepts it and the sidecar is fresh, meaning at least as new as the
// raw file, so a stale sidecar from an older build never shadows the real
// bytes. Embedded files share one zero modtime, so the pair is always
// same-aged there. The build precompresses the one asset that matters:
// gridwell.wasm is tens of megabytes raw and a fraction of that gzipped, and a
// phone on a relayed link downloads it on every boot. Content-Type comes from
// the raw file's extension, because instantiateStreaming requires
// application/wasm, and ServeContent still handles If-Modified-Since so
// browser caching keeps working.
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
	// The decoded size: with Content-Encoding the browser knows only the
	// compressed length, so the boot overlay could count bytes but not a
	// percentage. The raw file's size is here, so hand it over; index.html
	// reads it for its loading percentage.
	w.Header().Set("X-Uncompressed-Size", strconv.FormatInt(raw.Size(), 10))
	http.ServeContent(w, r, path.Base(name), gzInfo.ModTime(), rs)
	return true
}

// The sentinel-to-class table lives in api/gwerr, next to the sentinel
// declarations, so every codec — the Connect one in asConnectError, and a
// namespace's own status mapping — maps from the one classification. Do not
// re-enumerate the sentinels here.
