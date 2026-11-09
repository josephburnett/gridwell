// Package server is the HTTP layer of Gridwell: Connect-RPC at
// /gridwell.v1.Gridwell/<Method>, the static web client at /, the shell
// WebSocket, so every host that runs the client has shells, and the MCP API
// for agents. Two APIs, WebHandler and ConnectionHandler; nodeexport.go
// declares both.
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
	"time"

	"connectrpc.com/connect"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/api/tracewire"
	"github.com/josephburnett/gridwell/client/shellwire"
	"github.com/josephburnett/gridwell/internal/eventhub"
	"github.com/josephburnett/gridwell/internal/mcpapi"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/subscription"
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
	// shell primitive from the + menu.
	DisableShells bool
	// WebAddr is where the web API listens, the origin a reference is
	// spelled under when no client named one (shellpaste.BindOrigin).
	WebAddr string
	// Dir is the node's directory node.Options names; the trace API writes its
	// dumps under it. Empty refuses a dump rather than guessing a directory.
	Dir string
}

// Sources is what a node declares, each in its slot: home, where a client
// lands; the files; and the transport, every connection under the node's id.
// A nil slot declares nothing.
type Sources struct {
	Home      namespace.Namespace
	Files     namespace.Namespace
	Transport namespace.Namespace
	// Switches turns a connection off; nil when the node has none.
	Switches Switches
}

// Switches is the user's switch over the connections, keyed by connection
// name: a connection is the only source that can be disabled
// (connection.Server).
type Switches interface {
	Disable(name string) error
	DisabledReason(name string) string
	DisabledNow() []string
}

// Server routes every operation to the source owning its qualified id
// (rpc.OwnerNamespaceOf). Mount WebHandler and ConnectionHandler on their own
// sockets; see node.Start.
type Server struct {
	cfg Config
	src Sources

	mux *http.ServeMux

	// shellWriteTimeout is this server's PTY-output write bound; see
	// defaultShellWriteTimeout.
	shellWriteTimeout time.Duration

	subscription *subscription.Book

	// shells is every open /shell attachment and the session it rides under.
	shells liveShells

	// switchedOff tells every open stream of a connection the user just
	// disabled (router.DisableSource); Sources.Switches owns which are.
	switchedOff *eventhub.Hub[*pb.Event]

	// build is tracewire.BuildCommit, read once: the handshake names it and
	// otherBuild judges every page's call against it.
	build string
}

// New refuses an empty Config.Password: the web API has no open mode.
func New(src Sources, cfg Config) (*Server, error) {
	if cfg.Password == "" {
		return nil, errors.New("server: a web password is required (the web API is never open)")
	}
	srv := &Server{
		cfg:               cfg,
		src:               src,
		mux:               http.NewServeMux(),
		shellWriteTimeout: defaultShellWriteTimeout,
		build:             tracewire.BuildCommit(),
		switchedOff:       eventhub.New(rpc.EventKey),
	}
	srv.subscription = subscription.New(srv.spreadSubscription)
	srv.routes()
	return srv, nil
}

// resolve is the routing lookup every API goes through: the namespace owning a
// qualified id (rpc.OwnerNamespaceOf), the local id it understands, the uuid
// its answers are re-qualified with, and whether it is transit.
// "<id>/<digits>" is the home store; "<id>/~<path>" is the files, answering
// bare paths qualified with the node's id; "<id>/<letters>/…" is a
// connection, whose local id keeps the connection segment for the transport
// to peel. The transport is the only transit namespace: its ids arrive
// already qualified from the far node's frame.
func (s *Server) resolve(id string) (ns namespace.Namespace, local, uuid string, transit, ok bool) {
	first, local, split := rpc.SplitID(id)
	if !split || first != s.cfg.ID {
		return nil, "", "", false, false
	}
	owner := rpc.OwnerNamespaceOf(id, s.cfg.ID)
	if owner != first && rpc.QualifierOf(owner) != first {
		return s.src.Transport, local, first, true, s.src.Transport != nil
	}
	ns, ok = s.owned(owner)
	return ns, local, first, false, ok
}

// owned is the namespace of this node's own whose owner id is owner: home or
// the files.
func (s *Server) owned(owner string) (namespace.Namespace, bool) {
	for _, n := range s.namespaces() {
		if !n.Transit && n.UUID == owner {
			return n.NS, true
		}
	}
	return nil, false
}

// undeclared is the answer for an id whose owner this node does not declare:
// the dead verdict (gwerr.DeadLink), whichever hop of a chain this node is.
func (s *Server) undeclared(id string) error {
	ns := rpc.OwnerNamespaceOf(id, s.cfg.ID)
	return gwerr.DeadLink(ns, "no source %q for %q", ns, id)
}

// clientForID resolves the namespace that owns a qualified id, and the local
// id.
func (s *Server) clientForID(id string) (ns namespace.Namespace, local string, ok bool) {
	c, local, _, _, found := s.resolve(id)
	return c, local, found
}

// namespaceRow is one namespace this node declares. UUID is its owner id
// (rpc.OwnerNamespaceOf), which keys its roster row, its health and its share
// of the subscription; Label names its roster row, empty for the transport,
// whose rows carry their own.
type namespaceRow struct {
	UUID, Label string
	NS          namespace.Namespace
	Transit     bool
}

// prefix is what the namespace's answers are qualified with: the node's id
// for the files (rpc.QualifierOf).
func (n namespaceRow) prefix() string { return rpc.QualifierOf(n.UUID) }

// namespaces enumerates what this node declares, in the one order every
// fan-out takes: home, the files, then the transport under the node's own id,
// which is the id a connection's rows re-qualify through. A node without an
// id declares nothing.
func (s *Server) namespaces() []namespaceRow {
	if s.cfg.ID == "" {
		return nil
	}
	var out []namespaceRow
	if s.src.Home != nil {
		out = append(out, namespaceRow{UUID: s.cfg.ID, Label: "home", NS: s.src.Home})
	}
	if s.src.Files != nil {
		out = append(out, namespaceRow{UUID: rpc.FilesOwner(s.cfg.ID), Label: "files", NS: s.src.Files})
	}
	if s.src.Transport != nil {
		out = append(out, namespaceRow{UUID: s.cfg.ID, NS: s.src.Transport, Transit: true})
	}
	return out
}

// homeScratch is the node's scratch grid, home's, qualified: where every
// ephemeral visit this node serves lands, whatever grid it was opened from.
// "" when the node has no home.
func (s *Server) homeScratch(ctx context.Context) (string, error) {
	if s.src.Home == nil || s.cfg.ID == "" {
		return "", nil
	}
	info, err := s.sourceInfo(ctx, s.cfg.ID)
	if err != nil || info.ScratchGridId == "" {
		return "", err
	}
	return rpc.QualifyID(s.cfg.ID, info.ScratchGridId), nil
}

// sourceInfo is the Info of the namespace whose owner id is uuid, home's or
// the files', bounded by sourceInfoTimeout so a hung one degrades to an error
// rather than a stall.
func (s *Server) sourceInfo(ctx context.Context, uuid string) (*pb.InfoResponse, error) {
	c, found := s.owned(uuid)
	if !found {
		return nil, errors.New("no source " + uuid)
	}
	ictx, cancel := context.WithTimeout(ctx, sourceInfoTimeout)
	defer cancel()
	return c.Info(ictx, &pb.InfoRequest{})
}

func (s *Server) routes() {
	// The web API decodes onto the one in-process router.
	path, handler := gridwellv1connect.NewGridwellHandler(newConnectHandler(newRouter(s)),
		connect.WithInterceptors(traceInterceptor{}))
	s.mux.Handle(path, handler)

	// One live PTY per WebSocket, on the same gated mux (shell_api.go).
	s.mux.Handle(shellwire.Path, s.shellAPI())

	// The agents' API (mcp_api.go), gated like the browser's.
	s.mux.Handle(mcpapi.Path, s.mcpAPI())

	// Exempt from the cookie gate; see content_api.go.
	s.mux.Handle(contentPathPrefix, s.contentAPI())

	// The diagnostic ring, gated like everything else here (trace_api.go).
	s.mux.Handle(tracewire.Path, s.traceAPI())
	s.mux.Handle(tracewire.DumpPath, s.traceDumpAPI())

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
// declarations, so every API maps from the one classification.
