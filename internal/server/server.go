// Package server is the HTTP layer of Gridwell. The RPC surface is
// served by a Connect-RPC handler at /gridwell.v1.Gridwell/<Method>
// (binary-proto and JSON-over-proto codecs both supported); the static
// web/ directory is served at /. Live URL tiles are hosted natively by the
// Electron shell (WebContentsView), and shell PTY bytes ride the Electron
// main process's gRPC OpenShell stream against the node export — the
// browser-facing surface is pure Connect.
//
// Single-tenant, two doors (2026-08-26): WebHandler is the browser surface,
// ALWAYS gated behind the password (the minted <home>/web-password file;
// auth.go: one cookie derived from the current password) and bindable to
// a network; FederationHandler is the raw-gRPC node export, ungated and
// served only on the 0600 unix socket node.listenFederation opens.
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
	"github.com/josephburnett/gridwell/internal/plugin"
	"strconv"
)

// Config configures the server.
type Config struct {
	// StaticFS is the filesystem served at / — normally the EMBEDDED web
	// client (web.FS: the binary is self-contained, 2026-08-12), or an
	// os.DirFS over a dev checkout when server.yaml/--static overrides.
	// Nil disables static files (headless: some tests, pure RPC probing).
	StaticFS fs.FS
	// ID is the node's own id — its home's namespace ("<ID>/12") and the
	// prefix of every connection through it ("<ID>/<conn>/…"). Empty in
	// unit tests over raw plugin routing (no connections then).
	ID string
	// Password gates the browser surface (the mux) behind the login-page
	// cookie — see auth.go. REQUIRED: New refuses an empty one. The web
	// door is never open (owner decision 2026-08-26) — BuildConfig mints
	// the password on first serve, so production never has an open path,
	// and this refusal is what keeps a test from having one either.
	Password string
	// DisableShells refuses shell tiles node-wide: CreateTile(kind=shell)
	// and OpenShell are denied for every plugin (local or mounted),
	// ShellSessionAlive answers "gone", and Handshake carries the flag so
	// the client drops the shell primitive from the + palette. The one
	// server-side owner of the fact is this field (from server.yaml
	// disable_shells).
	DisableShells bool
}

// Server is the wired-up HTTP server: a router. Every operation is routed
// through the registry by the first segment of its qualified id; home (the
// first configured entry with a root) is where a client lands. Construct
// with New and mount WebHandler (the browser door) and FederationHandler
// (the node door) on their own listeners (node.Start).
type Server struct {
	cfg       Config
	pluginReg *plugin.Registry

	mux *http.ServeMux

	// infoCache memoizes each plugin's first successful Info handshake, keyed
	// by plugin uuid. Identity, roots, and capabilities are stable for a
	// plugin's lifetime, so repeat Handshake / Subscribe calls must not
	// re-handshake every plugin (a consistently slow remote made every
	// palette open pay pluginInfoTimeout). Failures are never cached — the
	// next call retries. Invalidated when a plugin's declared facts change
	// under one uuid (invalidateInfoCache: a SetRootView framing write).
	infoMu    sync.Mutex
	infoCache map[string]*pb.InfoResponse
}

// New constructs a Server that routes everything through reg; every
// operation is addressed by a qualified id. An empty Password is refused:
// the browser door has no open mode.
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

// routeClient resolves a namespace uuid to its client (home or a plugin).
// Connections are not addressable by uuid alone — they live under the
// node's id ("<ID>/<conn>/…"); resolve is the lookup that sees them.
func (s *Server) routeClient(uuid string) (pb.GridwellClient, bool) {
	return s.pluginReg.Get(uuid)
}

// resolve is THE routing lookup: the namespace that owns a qualified id,
// its client, the local (unprefixed) id the namespace understands, the
// uuid to re-qualify answers with, and whether the namespace is TRANSIT
// (its ids are chains from another node — the transit qualification
// rule). "<ID>/<digits>" is the home store; "<ID>/<letters>/…" is a
// connection, owned by the transport, whose local id keeps the
// connection segment ("<conn>/…" — the transport peels it); anything
// else is a plugin by uuid. The Connect handler, the export's streams,
// the content door and the shell relay all resolve through here.
func (s *Server) resolve(id string) (client pb.GridwellClient, local, uuid string, transit, ok bool) {
	uuid, local, split := rpc.SplitID(id)
	if !split {
		return nil, "", "", false, false
	}
	if s.cfg.ID != "" && uuid == s.cfg.ID && !localIsHome(local) {
		t, has := s.pluginReg.Transport()
		return t, local, uuid, true, has
	}
	c, found := s.pluginReg.Get(uuid)
	return c, local, uuid, s.pluginReg.Transit(uuid), found
}

// localIsHome reports whether a local id under the node's own segment
// names the HOME store (its first segment is numeric — a grid or tile id)
// rather than a connection (a letter-leading name; the id-shape rule).
func localIsHome(local string) bool {
	first := local
	if i := strings.IndexByte(local, '/'); i >= 0 {
		first = local[:i]
	}
	if first == "" {
		return true
	}
	for i := 0; i < len(first); i++ {
		if first[i] < '0' || first[i] > '9' {
			return false
		}
	}
	return true
}

// clientForID resolves the namespace that owns a qualified id, returning
// its client and the local id. Used by the shell + preview infrastructure
// to address a tile wherever it lives.
func (s *Server) clientForID(id string) (client pb.GridwellClient, local string, ok bool) {
	c, local, _, _, found := s.resolve(id)
	return c, local, found
}

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
// cache entry must be dropped to reflect the new framing on the next Handshake
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

	// (The shell PTY WebSocket bridge is gone — 2026-07-26: PTY bytes ride
	// the Electron main process's gRPC OpenShell stream against the node
	// export; browsers show frozen shell previews, caps-gated.)

	// (The /session/ door is gone — 2026-07-26: the Chromium session is
	// host-local; nothing hydrates or dehydrates a plugin session blob.)

	// The /content/ door: plugin-served web content (see content_door.go).
	// Registered on the mux but exempted from the cookie gate — the content
	// token in the path is the credential there.
	s.mux.Handle(contentPathPrefix, s.contentDoor())

	if s.cfg.StaticFS != nil {
		s.mux.Handle("/", s.staticOrSPA(s.cfg.StaticFS))
	}
}

// staticOrSPA serves files from fsys — the embedded web client or a dev
// checkout — falling back to index.html for any request that doesn't match
// a file (the SPA path grammar). The /rpc/ prefix — the pre-Connect RPC
// namespace — stays a hard 404 so a stale caller gets an error, never HTML.
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
// client accepts it and the sidecar is FRESH (at least as new as the raw
// file — a stale sidecar from an older build must never shadow the real
// bytes; embedded files share one zero modtime, so the pair is always
// same-aged there). The build precompresses the one asset that matters:
// gridwell.wasm is ~33 MB raw and ~6.5 MB gzipped, and a phone on a
// relayed tailscale link downloads it on every boot — uncompressed, that
// is minutes of blank page. Content-Type comes from the RAW file's
// extension (instantiateStreaming requires application/wasm); ServeContent
// still handles If-Modified-Since so browser caching keeps working.
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
		return false // ServeContent needs seeking; embed.FS and os.DirFS both provide it
	}
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Set("Vary", "Accept-Encoding")
	// The DECODED size: with Content-Encoding the browser only knows the
	// compressed length, so the boot overlay could count bytes but not a
	// percentage. The raw file's size is sitting right here — hand it over
	// (index.html reads it for "loading gridwell… N%").
	w.Header().Set("X-Uncompressed-Size", strconv.FormatInt(raw.Size(), 10))
	http.ServeContent(w, r, path.Base(name), gzInfo.ModTime(), rs)
	return true
}

// The sentinel→class table lives in internal/store (gwerr.ClassifyError),
// next to the sentinel declarations, so every transport — Connect
// (asConnectError) and the plugin gRPC hop (local.errToStatus) — maps
// from the one classification. Do not re-enumerate the sentinels here.
