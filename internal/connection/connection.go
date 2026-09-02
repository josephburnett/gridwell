// Package connection is the node's transport: its connections to other nodes. A
// connection is config — a server.yaml `connections:` row naming it, labelling
// it, and saying how to dial. The transport dials each one, learns where it
// lands, which is the far node's home, and routes every id shaped
// "<conn>/<remote-id…>" to that connection's client, prepending the segment on
// the way back through rpc.TransitQualifyTiles: the one transit rule, the same
// one the node applies a level up under its own id.
//
// The transport is not a plugin and owns no tiles. A connection is a row in
// the + menu and, when the user drags it, an ordinary link tile in their own
// grid. What it remembers, in db.go, is the learned landing and the graveyard
// of retired names.
package connection

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/idshape"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/connection/dial"
	"github.com/josephburnett/gridwell/internal/eventhub"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// Dialer builds a namespace over a remote node's export from a resolved
// config. Production is dial.Dial, whose ssh session is lazy and self-healing,
// and which reads the far node's gridwell.v1 through the one client codec,
// namespace.FromClient. Tests inject in-process nodes.
type Dialer func(cfg dial.Config) (namespace.Namespace, func(), error)

// bootDialWait bounds how long ConnectAll waits for each connection at boot
// before serving anyway. The dial keeps trying in the background.
var bootDialWait = 5 * time.Second

// Server is the transport: a namespace.Namespace whose ids are chains
// through its connections — each connection itself a Namespace, read off
// the far node's connection door by namespace.FromClient.
type Server struct {
	namespace.Unimplemented

	db   *DB
	dial Dialer
	home string // the host's home dir, for ~-relative key defaults

	// conns is the declared set, by name, in config order (order).
	conns map[string]*Conn
	order []string

	mu   sync.Mutex
	live map[string]*liveConn // by name
	// rootErr is a connection's last dial or root-fetch failure, by name: the
	// one fact behind a pending row's status. Written by ensureLive and the
	// root learn, cleared on success, never persisted.
	rootErr map[string]string

	hub *eventhub.Hub[*gridwellv1.Event]
}

// Conn is one declared connection with what the store remembers about it.
type Conn struct {
	Cfg        config.ConnectionConfig
	RemoteRoot string // the learned landing (the far node's home grid, in ITS frame); "" until learned
}

// liveConn is one connection's constructed transport. Constructing is cheap
// and non-blocking, because the ssh layer is lazy, so a liveConn exists as
// soon as the connection dialed.
type liveConn struct {
	client namespace.Namespace
	closer func()
	cancel context.CancelFunc // stops the root-fetch/fan-in goroutines
	// rootFetching single-flights the remote-root learn.
	rootFetching bool
}

// Row is a connection as the handshake lists it (the node qualifies the
// uuid with its own id).
type Row struct {
	Name         string
	Label        string
	RootGridID   string // "<name>/<remote home>" once learned, "" while pending
	StatusDetail string // the last dial/learn failure while pending
	ViewCx       float64
	ViewCy       float64
	ViewZoom     float64
}

// The router calls the transport as a Go value; the compiler says so.
var _ namespace.Namespace = (*Server)(nil)

// New builds the transport and reconciles the store against the declared
// connections; server.yaml is authoritative. A declared name that is retired,
// whether in retired or tombstoned in the store, is refused; a stored name the
// config does not declare is tombstoned; and every retired name is reserved in
// the store forever. home is the host's home directory, and "" means no ~
// defaults, so keys must be explicit paths.
//
// It is also the boot gate on the connections' host-local config: every
// declared row must resolve to a dial plan whose files are there. A row that
// cannot is an error, so `serve` refuses to start rather than coming up with
// a connection that could never have dialed.
func New(db *DB, dialer Dialer, home string, conns []config.ConnectionConfig, retired []string) (*Server, error) {
	ctx := context.Background()
	s := &Server{db: db, dial: dialer, home: home, conns: map[string]*Conn{},
		live: map[string]*liveConn{}, rootErr: map[string]string{}, hub: eventhub.New(eventKey)}
	retiredSet := map[string]bool{}
	for _, r := range retired {
		retiredSet[r] = true
	}
	for _, c := range conns {
		if err := idshape.ValidateSegment("connection name", c.Name); err != nil {
			return nil, err
		}
		if _, dup := s.conns[c.Name]; dup {
			return nil, fmt.Errorf("connection %q declared twice", c.Name)
		}
		if retiredSet[c.Name] {
			return nil, fmt.Errorf("connection %q: this name is RETIRED — a retired name never returns; mint a new one", c.Name)
		}
		// The host-local half of the row, checked before the node serves:
		// a missing addr, a key or known_hosts path that is not there or
		// not readable. Those are facts this machine can settle, and a
		// connection that can never dial is a misconfiguration to fail on,
		// not a row that comes up quietly dark. Whether the far node
		// answers is deliberately NOT asked here — a laptop on a plane
		// still serves its home and its cache; see ConnectAll.
		if _, err := s.dialConfig(c); err != nil {
			return nil, fmt.Errorf("connection %q: %w", c.Name, err)
		}
		row, err := db.Get(ctx, c.Name)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		if err == nil && row.Deleted {
			return nil, fmt.Errorf("connection %q: this name is RETIRED in the connection store — a retired name never returns; mint a new one", c.Name)
		}
		if err := db.Ensure(ctx, c.Name); err != nil {
			return nil, err
		}
		s.conns[c.Name] = &Conn{Cfg: c, RemoteRoot: row.RemoteRoot}
		s.order = append(s.order, c.Name)
	}
	rows, err := db.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if _, declared := s.conns[r.Name]; !declared && !r.Deleted {
			if err := db.Tombstone(ctx, r.Name); err != nil {
				return nil, fmt.Errorf("retire connection %q: %w", r.Name, err)
			}
		}
	}
	for _, name := range retired {
		if err := db.Tombstone(ctx, name); err != nil {
			return nil, fmt.Errorf("reserve retired name %q: %w", name, err)
		}
	}
	return s, nil
}

// Close tears down every live connection and closes the store.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name, lc := range s.live {
		lc.cancel()
		lc.closer()
		delete(s.live, name)
	}
	return s.db.Close()
}

// ConnectAll dials every declared connection and learns its landing, bounded
// per connection by bootDialWait, so the boot serves no mysteries: a reachable
// connection is live with its root known before the node serves, and an
// unreachable one has its error in the log and on its row and keeps trying
// lazily on every read.
func (s *Server) ConnectAll(ctx context.Context) {
	for _, name := range s.order {
		c := s.conns[name]
		done := make(chan error, 1)
		go func() { _, err := s.learnRoot(c); done <- err }()
		select {
		case err := <-done:
			if err != nil {
				log.Printf("gridwell: connection %q (%s): %v", c.Cfg.Label, name, err)
			} else {
				log.Printf("gridwell: connection %q (%s): connected — root %s", c.Cfg.Label, name, c.RemoteRoot)
			}
		case <-time.After(bootDialWait):
			log.Printf("gridwell: connection %q (%s): no answer after %v — still trying in the background", c.Cfg.Label, name, bootDialWait)
		case <-ctx.Done():
			return
		}
	}
}

// Rows lists the declared connections for the handshake, in config order:
// label, landing once learned, the pending failure, and the remote home's
// persisted view, asked of a live remote briefly. A dark one contributes
// zeros.
func (s *Server) Rows(ctx context.Context) []Row {
	out := make([]Row, 0, len(s.order))
	for _, name := range s.order {
		c := s.conns[name]
		s.kickRootFetch(c)
		r := Row{Name: name, Label: c.Cfg.Label}
		if r.Label == "" {
			r.Label = name
		}
		s.mu.Lock()
		root := c.RemoteRoot
		lc := s.live[name]
		r.StatusDetail = s.rootErr[name]
		s.mu.Unlock()
		if root != "" {
			r.RootGridID = rpc.QualifyID(name, root)
			r.StatusDetail = ""
			if lc != nil {
				vctx, cancel := context.WithTimeout(ctx, time.Second)
				if lp, err := lc.client.Handshake(vctx, &gridwellv1.HandshakeRequest{}); err == nil {
					r.ViewCx, r.ViewCy, r.ViewZoom = lp.HomeViewCx, lp.HomeViewCy, lp.HomeViewZoom
				}
				cancel()
			}
		}
		out = append(out, r)
	}
	return out
}

// ── routing ──────────────────────────────────────────────────────────────────

// routePlaceholder stands in for the local half of an id when only the
// connection matters.
const routePlaceholder = "0"

// forward is a resolved hop: the connection's client plus its name for
// prepending response ids.
type forward struct {
	ns     string
	client namespace.Namespace
}

// route resolves the connection an id chains through: the first segment is the
// connection name, and the rest is the far node's own id, forwarded verbatim
// whatever shape its segments take. A TILE segment in first position — a row
// id, or a key form naming an entry on the far node — is malformed, because
// the transport owns no tiles of its own; rpc.IsTileSegment is the same
// classifier the router's peel and the URL grammar ask.
func (s *Server) route(ctx context.Context, id string) (*forward, string, error) {
	first, rest, ok := rpc.SplitID(id)
	if !ok {
		return nil, "", status.Errorf(codes.InvalidArgument, "connection: id %q names no connection", id)
	}
	if rpc.IsTileSegment(first) {
		return nil, "", status.Errorf(codes.InvalidArgument, "connection: id %q chains through a tile segment", id)
	}
	c, ok := s.conns[first]
	if !ok {
		if row, err := s.db.Get(ctx, first); err == nil && row.Deleted {
			return nil, "", status.Errorf(codes.NotFound, "connection: connection %q was retired", first)
		}
		return nil, "", status.Errorf(codes.NotFound, "connection: no connection %q", first)
	}
	lc, err := s.ensureLive(c)
	if err != nil {
		return nil, "", err
	}
	return &forward{ns: first, client: lc.client}, rest, nil
}

// dialConfig resolves a declared connection to a dial.Config, applying the
// host-side defaults: port 22; the key is the first of ~/.ssh/id_ed25519 and
// ~/.ssh/id_rsa that exists; known_hosts is ~/.ssh/known_hosts. addr, the
// far node's connection-door socket path, is required either way, because that
// socket lives under the far node's home, which only the operator knows.
//
// It is the one owner of what a connection's fields mean, so it is also the
// one gate on them: the plan it returns is checked, dial.Config.Check, and
// every host-local file it names is there and readable. New calls it for
// every declared connection at boot, which is how a bad path fails `serve`
// instead of leaving the connection dark.
func (s *Server) dialConfig(c config.ConnectionConfig) (dial.Config, error) {
	cfg := dial.Config{
		User:       c.User,
		KeyPath:    expandHome(c.Key, s.home),
		KnownHosts: expandHome(c.KnownHosts, s.home),
		Addr:       c.Addr,
	}
	if cfg.Addr == "" {
		return dial.Config{}, fmt.Errorf("addr required — the remote node's connection-door socket path (its <home>/federation.sock)")
	}
	if c.Host == "" {
		return cfg, nil // a direct dial of the socket: nothing host-local to check
	}
	if strings.TrimSpace(c.User) == "" {
		return dial.Config{}, fmt.Errorf("user is required for an ssh connection")
	}
	port := int64(22)
	if c.Port != 0 {
		if c.Port < 1 || c.Port > 65535 {
			return dial.Config{}, fmt.Errorf("port must be in 1..65535, got %d", c.Port)
		}
		port = c.Port
	}
	cfg.Host = fmt.Sprintf("%s:%d", c.Host, port)
	if cfg.KeyPath == "" {
		if s.home == "" {
			return dial.Config{}, fmt.Errorf("key path required (no home directory to default from)")
		}
		cfg.KeyPath = firstExisting(filepath.Join(s.home, ".ssh", "id_ed25519"), filepath.Join(s.home, ".ssh", "id_rsa"))
	}
	if cfg.KnownHosts == "" {
		if s.home == "" {
			return dial.Config{}, fmt.Errorf("known_hosts path required (no home directory to default from)")
		}
		cfg.KnownHosts = filepath.Join(s.home, ".ssh", "known_hosts")
	}
	if err := cfg.Check(); err != nil {
		return dial.Config{}, err
	}
	return cfg, nil
}

func expandHome(p, home string) string {
	if home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// firstExisting returns the first path that exists, or else the first path,
// whose open failure then names the expected default loudly.
func firstExisting(paths ...string) string {
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return paths[0]
}

// ensureLive returns the connection's transport, constructing it on first
// use. New already refused a config-shaped problem at boot, so the same
// check here catches only what changed underneath a running node — a key
// file deleted or chmod'ed away — and it surfaces loudly, on every attempt.
func (s *Server) ensureLive(c *Conn) (*liveConn, error) {
	name := c.Cfg.Name
	s.mu.Lock()
	defer s.mu.Unlock()
	if lc, ok := s.live[name]; ok {
		return lc, nil
	}
	cfg, err := s.dialConfig(c.Cfg)
	if err != nil {
		s.rootErr[name] = err.Error()
		return nil, status.Errorf(codes.FailedPrecondition, "connection: connection %q: %v", name, err)
	}
	if s.dial == nil {
		s.rootErr[name] = "no dialer"
		return nil, status.Errorf(codes.FailedPrecondition, "connection: connection %q: no dialer", name)
	}
	client, closer, err := s.dial(cfg)
	if err != nil {
		// Record the bare dial error: the wrapper is routing noise to
		// whoever reads the row status.
		s.rootErr[name] = err.Error()
		return nil, status.Errorf(codes.Unavailable, "connection: connection %q: %v", name, err)
	}
	delete(s.rootErr, name) // transport constructed; the learn may still fail
	ctx, cancel := context.WithCancel(context.Background())
	lc := &liveConn{client: client, closer: closer, cancel: cancel}
	s.live[name] = lc
	// Remote change events flow from the moment the connection is live,
	// prefixed with its segment: the same fan-in shape the node runs per
	// namespace, one level down.
	go s.fanInRemote(ctx, name, client)
	return lc, nil
}

// setRootErr records a connection's last dial or root-fetch failure; "" clears
// it.
func (s *Server) setRootErr(name, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if detail == "" {
		delete(s.rootErr, name)
		return
	}
	s.rootErr[name] = detail
}

// learnRoot is the connect-and-learn body: the boot path, ConnectAll, calls it
// synchronously, and the lazy kick, kickRootFetch, calls it in a goroutine. It
// dials the transport; a learned root is final, and a fresh one persists and
// publishes a health event so open clients re-list.
func (s *Server) learnRoot(c *Conn) (string, error) {
	name := c.Cfg.Name
	lc, err := s.ensureLive(c)
	if err != nil {
		return "", err // ensureLive recorded the detail already
	}
	s.mu.Lock()
	root := c.RemoteRoot
	s.mu.Unlock()
	if root != "" {
		return root, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	info, err := lc.client.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		s.setRootErr(name, status.Convert(err).Message())
		return "", err
	}
	if info.RootGridId == "" {
		err := errors.New("the connected node declared no home")
		s.setRootErr(name, err.Error())
		return "", err
	}
	s.setRootErr(name, "")
	if err := s.db.SetRemoteRoot(ctx, name, info.RootGridId); err != nil {
		return "", err
	}
	s.mu.Lock()
	c.RemoteRoot = info.RootGridId
	s.mu.Unlock()
	s.hub.Publish(&gridwellv1.Event{Payload: &gridwellv1.Event_PluginHealth{PluginHealth: &gridwellv1.EventPluginHealth{
		PluginUuid: name, Healthy: true,
	}}})
	return info.RootGridId, nil
}

// kickRootFetch learns a connection's landing in the background,
// single-flight per connection; a no-op once learned.
func (s *Server) kickRootFetch(c *Conn) {
	s.mu.Lock()
	known := c.RemoteRoot != ""
	s.mu.Unlock()
	if known {
		return
	}
	lc, err := s.ensureLive(c)
	if err != nil {
		return // recorded in rootErr; the row says why
	}
	s.mu.Lock()
	if lc.rootFetching {
		s.mu.Unlock()
		return
	}
	lc.rootFetching = true
	s.mu.Unlock()
	go func() {
		defer func() {
			s.mu.Lock()
			if l, ok := s.live[c.Cfg.Name]; ok {
				l.rootFetching = false
			}
			s.mu.Unlock()
		}()
		_, _ = s.learnRoot(c)
	}()
}

// fanInRemote forwards a connection's remote change events, each id prefixed
// with the connection segment. It is a plain retry loop: the transport
// underneath self-heals through the redialer, so a dropped stream re-subscribes
// — never silently, since each transition is logged and published as an
// EventPluginHealth, the same contract the node's fanInEvents keeps per
// namespace.
func (s *Server) fanInRemote(ctx context.Context, ns string, client namespace.Namespace) {
	healthy := true
	report := func(up bool, detail string) {
		s.hub.Publish(&gridwellv1.Event{Payload: &gridwellv1.Event_PluginHealth{PluginHealth: &gridwellv1.EventPluginHealth{
			PluginUuid: ns, Healthy: up, Detail: detail,
		}}})
	}
	for {
		// Established, not opened: a callback stream has no open to report,
		// so namespace.Follow decides the moment, and the node's own fan-in
		// reads the same definition.
		err := namespace.Follow(ctx, client, &gridwellv1.SubscribeRequest{},
			func(ev *gridwellv1.Event) error {
				s.hub.Publish(rpc.TransitQualifyEvent(ns, ev))
				return nil
			},
			func() {
				if !healthy {
					healthy = true
					report(true, "")
				}
			})
		if ctx.Err() != nil {
			return
		}
		detail := "the connection's event stream ended"
		if err != nil {
			detail = err.Error()
		}
		log.Printf("gridwell: connection %s: event stream ended: %v (retrying in 5s)", ns, detail)
		if healthy {
			healthy = false
			report(false, detail)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// ── the forwarded verbs ──────────────────────────────────────────────────────

// Handshake forwards a namespaced request through the named connection: peel
// the connection segment, forward the rest to its node export, and re-qualify
// the answer with the segment.
//
// With no namespace it answers for the transport itself, which is what the
// handshake means everywhere: "tell me about the namespace I am talking to."
// The transport owns no tiles, so what it has to declare is its connections —
// each with the landing it has learned, in the transport's own frame
// ("<conn>/<remote id>"). Rows is the one owner of that fact; the node's own
// handshake row-builds from the same call, one qualifying hop up. It is the
// door anything fronting this namespace asks, the source cache's whole-source
// walk included: a namespace that owns no grids still knows where its content
// begins.
func (s *Server) Handshake(ctx context.Context, req *gridwellv1.HandshakeRequest) (*gridwellv1.HandshakeResponse, error) {
	ns := req.GetNamespace()
	if ns == "" {
		resp := &gridwellv1.HandshakeResponse{}
		for _, r := range s.Rows(ctx) {
			resp.Connections = append(resp.Connections, &gridwellv1.ConnectionInfo{
				Uuid: r.Name, Label: r.Label, RootGridId: r.RootGridID,
				RootViewCx: r.ViewCx, RootViewCy: r.ViewCy, RootViewZoom: r.ViewZoom,
				StatusDetail: r.StatusDetail,
			})
		}
		return resp, nil
	}
	first, rest, ok := rpc.SplitID(ns)
	if !ok {
		first, rest = ns, ""
	}
	// A handshake names a connection, not a tile, so route gets the
	// connection segment plus a row-shaped placeholder: route only needs to
	// see a well-formed chain, and "0" is the cheapest tile segment there is.
	fw, _, err := s.route(ctx, first+"/"+routePlaceholder)
	if err != nil {
		return nil, err
	}
	resp, err := fw.client.Handshake(ctx, &gridwellv1.HandshakeRequest{Namespace: rest})
	if err != nil {
		return nil, err
	}
	return rpc.TransitQualifyPluginList(first, resp), nil
}

func (s *Server) Probe(ctx context.Context, req *gridwellv1.ProbeRequest) (*gridwellv1.ProbeResponse, error) {
	fw, local, err := s.route(ctx, req.TileId)
	if err != nil {
		// A connection that cannot be resolved is not gone; only a retired
		// one is. A failed read must never sweep a tile.
		if first, _, ok := rpc.SplitID(req.TileId); ok {
			if row, derr := s.db.Get(ctx, first); derr == nil && row.Deleted {
				return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_GONE}, nil
			}
		}
		return nil, err
	}
	return fw.client.Probe(ctx, &gridwellv1.ProbeRequest{TileId: local})
}

// SetFraming forwards the one framing write, routed on whichever target
// the request names — a doorway tile or a root grid.
func (s *Server) SetFraming(ctx context.Context, req *gridwellv1.SetFramingRequest) (*gridwellv1.SetFramingResponse, error) {
	ref := req.TileId
	if ref == "" {
		ref = req.RootGridId
	}
	fw, local, err := s.route(ctx, ref)
	if err != nil {
		return nil, err
	}
	out := proto.Clone(req).(*gridwellv1.SetFramingRequest)
	if out.TileId != "" {
		out.TileId = local
	} else {
		out.RootGridId = local
	}
	return fw.client.SetFraming(ctx, out)
}

func (s *Server) GetGrid(ctx context.Context, req *gridwellv1.GetGridRequest) (*gridwellv1.GetGridResponse, error) {
	fw, local, err := s.route(ctx, req.GridId)
	if err != nil {
		return nil, err
	}
	resp, err := fw.client.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: local})
	if err != nil {
		return nil, err
	}
	return &gridwellv1.GetGridResponse{
		// The one transit grid rule, shared with the node's hop.
		Grid:  rpc.TransitQualifyGrid(fw.ns, resp.Grid),
		Tiles: rpc.TransitQualifyTiles(fw.ns, resp.Tiles),
	}, nil
}

func (s *Server) GetTile(ctx context.Context, req *gridwellv1.GetTileRequest) (*gridwellv1.TileResponse, error) {
	fw, local, err := s.route(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	resp, err := fw.client.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: local})
	if err != nil {
		return nil, err
	}
	return prependTileResp(fw.ns, resp), nil
}

func (s *Server) GetTilePreview(ctx context.Context, req *gridwellv1.GetTilePreviewRequest) (*gridwellv1.GetTilePreviewResponse, error) {
	fw, local, err := s.route(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	return fw.client.GetTilePreview(ctx, &gridwellv1.GetTilePreviewRequest{TileId: local})
}

func (s *Server) ShellSessionAlive(ctx context.Context, req *gridwellv1.ShellSessionAliveRequest) (*gridwellv1.ShellSessionAliveResponse, error) {
	fw, local, err := s.route(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	return fw.client.ShellSessionAlive(ctx, &gridwellv1.ShellSessionAliveRequest{TileId: local})
}

func (s *Server) CreateTile(ctx context.Context, req *gridwellv1.CreateTileRequest) (*gridwellv1.TileResponse, error) {
	fw, local, err := s.route(ctx, req.GridId)
	if err != nil {
		return nil, err
	}
	out := proto.Clone(req).(*gridwellv1.CreateTileRequest)
	out.GridId = local
	if out.Tile != nil {
		// A qualified child or target crossing into the connection was
		// qualified from this side, so strip our segment and let the remote
		// see its own frame. The node's link machinery does the same strip
		// one level up.
		out.Tile.ChildGridId = stripPrefix(out.Tile.ChildGridId, fw.ns)
		out.Tile.LinkTargetId = stripPrefix(out.Tile.LinkTargetId, fw.ns)
	}
	resp, err := fw.client.CreateTile(ctx, out)
	if err != nil {
		return nil, err
	}
	return prependTileResp(fw.ns, resp), nil
}

func (s *Server) SetTile(ctx context.Context, req *gridwellv1.SetTileRequest) (*gridwellv1.TileResponse, error) {
	fw, local, err := s.route(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	out := proto.Clone(req).(*gridwellv1.SetTileRequest)
	out.TileId = local
	resp, err := fw.client.SetTile(ctx, out)
	if err != nil {
		return nil, err
	}
	return prependTileResp(fw.ns, resp), nil
}

func (s *Server) PlaceTile(ctx context.Context, req *gridwellv1.PlaceTileRequest) (*gridwellv1.TileResponse, error) {
	fw, local, err := s.route(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	out := proto.Clone(req).(*gridwellv1.PlaceTileRequest)
	out.TileId = local
	out.GridId = stripPrefix(out.GridId, fw.ns)
	resp, err := fw.client.PlaceTile(ctx, out)
	if err != nil {
		return nil, err
	}
	return prependTileResp(fw.ns, resp), nil
}

func (s *Server) CloneTile(ctx context.Context, req *gridwellv1.CloneTileRequest) (*gridwellv1.TileResponse, error) {
	fw, local, err := s.route(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	out := proto.Clone(req).(*gridwellv1.CloneTileRequest)
	out.TileId = local
	out.DestGridId = stripPrefix(out.DestGridId, fw.ns)
	resp, err := fw.client.CloneTile(ctx, out)
	if err != nil {
		return nil, err
	}
	return prependTileResp(fw.ns, resp), nil
}

func (s *Server) DeleteTile(ctx context.Context, req *gridwellv1.DeleteTileRequest) (*gridwellv1.DeleteTileResponse, error) {
	fw, local, err := s.route(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	out := proto.Clone(req).(*gridwellv1.DeleteTileRequest)
	out.TileId = local
	return fw.client.DeleteTile(ctx, out)
}

func (s *Server) ReadContent(ctx context.Context, req *gridwellv1.ReadContentRequest, send func(*gridwellv1.ContentChunk) error) error {
	fw, local, err := s.route(ctx, req.TileId)
	if err != nil {
		return err
	}
	return fw.client.ReadContent(ctx, &gridwellv1.ReadContentRequest{TileId: local}, send)
}

func (s *Server) ServeContent(ctx context.Context, req *gridwellv1.ServeContentRequest, send func(*gridwellv1.ServeContentChunk) error) error {
	fw, local, err := s.route(ctx, req.TileId)
	if err != nil {
		return err
	}
	return fw.client.ServeContent(ctx, &gridwellv1.ServeContentRequest{TileId: local, Subpath: req.Subpath}, send)
}

func (s *Server) WriteContent(ctx context.Context, recv func() (*gridwellv1.WriteContentRequest, error)) (*gridwellv1.TileResponse, error) {
	first, err := recv()
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "connection: write: empty stream")
	}
	if first.TileId == "" {
		return nil, status.Error(codes.InvalidArgument, "connection: write: first message must bind tile_id")
	}
	fw, local, err := s.route(ctx, first.TileId)
	if err != nil {
		return nil, err
	}
	// Clone before rewriting: with no wire between the caller and this hop,
	// the request is the caller's own message. See namespace.Namespace's
	// ownership contract.
	rewritten := proto.Clone(first).(*gridwellv1.WriteContentRequest)
	rewritten.TileId = local
	sentFirst := false
	resp, err := fw.client.WriteContent(ctx, func() (*gridwellv1.WriteContentRequest, error) {
		if !sentFirst {
			sentFirst = true
			return rewritten, nil
		}
		return recv()
	})
	if err != nil {
		return nil, err
	}
	return prependTileResp(fw.ns, resp), nil
}

func (s *Server) OpenShell(ctx context.Context, recv func() (*gridwellv1.OpenShellRequest, error), send func(*gridwellv1.OpenShellResponse) error) error {
	first, err := recv()
	if err != nil {
		return err
	}
	fw, local, err := s.route(ctx, first.TileId)
	if err != nil {
		return err
	}
	// Clone before rewriting the bind: the caller still owns `first`.
	rewritten := proto.Clone(first).(*gridwellv1.OpenShellRequest)
	rewritten.TileId = local
	sentBind := false
	return fw.client.OpenShell(ctx, func() (*gridwellv1.OpenShellRequest, error) {
		if !sentBind {
			sentBind = true
			return rewritten, nil
		}
		return recv()
	}, send)
}

// Subscribe streams every connection's remote events (prefixed) and the
// connections' own health transitions.
func (s *Server) Subscribe(ctx context.Context, _ *gridwellv1.SubscribeRequest, send func(*gridwellv1.Event) error) error {
	ch, cancel := s.hub.Subscribe()
	defer cancel()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			// The hub hands the same event to every subscriber and nobody
			// mutates it; the router's qualification clones. See
			// namespace's ownership contract.
			if err := send(ev); err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// Search forwards the one find verb through the transport. An id: query routes
// to the connection owning the id; free text fans out to live connections
// only, because a search answers with what is reachable and never dials the
// world. Each remote answer comes from a node, which fans out to its own
// namespaces, so the recursion through connections falls out of the chain shape. A
// connection that errors or times out contributes nothing, loudly.
func (s *Server) Search(ctx context.Context, req *gridwellv1.SearchRequest) (*gridwellv1.SearchResponse, error) {
	if q := rpc.ParseSearchQuery(req.Query); q.ID != "" {
		fw, local, err := s.route(ctx, q.ID)
		if err != nil {
			return nil, err
		}
		resp, err := fw.client.Search(ctx, &gridwellv1.SearchRequest{Query: "id:" + local, Limit: req.Limit})
		if err != nil {
			return nil, err
		}
		return prependSearchResp(fw.ns, resp), nil
	}
	s.mu.Lock()
	hops := make([]forward, 0, len(s.live))
	for ns, lc := range s.live {
		hops = append(hops, forward{ns, lc.client})
	}
	s.mu.Unlock()
	sort.Slice(hops, func(i, j int) bool { return hops[i].ns < hops[j].ns })
	out := &gridwellv1.SearchResponse{}
	for _, hp := range hops {
		// Each hop is bounded by rpc.SearchHopTimeout, shared with the
		// node's fan-out, so one hung tunnel cannot stall the search.
		hctx, cancel := context.WithTimeout(ctx, rpc.SearchHopTimeout)
		resp, err := hp.client.Search(hctx, &gridwellv1.SearchRequest{Query: req.Query, Limit: req.Limit})
		cancel()
		if err != nil {
			log.Printf("gridwell: search: connection %s skipped: %v", hp.ns, err)
			continue
		}
		out.Results = append(out.Results, prependSearchResp(hp.ns, resp).Results...)
	}
	return out, nil
}

// ── helpers ──────────────────────────────────────────────────────────────────

func prependSearchResp(ns string, resp *gridwellv1.SearchResponse) *gridwellv1.SearchResponse {
	return rpc.QualifySearchResponse(resp, func(ts []*gridwellv1.Tile) []*gridwellv1.Tile {
		return rpc.TransitQualifyTiles(ns, ts)
	})
}

func prependTileResp(ns string, resp *gridwellv1.TileResponse) *gridwellv1.TileResponse {
	t := resp.GetTile()
	if t == nil {
		return resp
	}
	return &gridwellv1.TileResponse{Tile: rpc.TransitQualifyTiles(ns, []*gridwellv1.Tile{t})[0]}
}

// stripPrefix removes "<ns>/" from an id qualified from this frame,
// leaving other ids untouched.
func stripPrefix(id, ns string) string {
	if strings.HasPrefix(id, ns+"/") {
		return id[len(ns)+1:]
	}
	return id
}

// eventKey names the entity a wire event is about, so the hub in
// internal/eventhub, shared with the home store, can replace an older
// undelivered event for the same entity with the newer one and never drop a
// distinct one. "" means unkeyable, and is never coalesced.
func eventKey(ev *gridwellv1.Event) string {
	switch p := ev.GetPayload().(type) {
	case *gridwellv1.Event_GridChanged:
		return "g/" + p.GridChanged.GetGridId()
	case *gridwellv1.Event_TileChanged:
		return "t/" + p.TileChanged.GetTile().GetId()
	case *gridwellv1.Event_TileRemoved:
		return "r/" + p.TileRemoved.GetGridId() + "/" + p.TileRemoved.GetTileId()
	case *gridwellv1.Event_PluginHealth:
		return "h/" + p.PluginHealth.GetPluginUuid()
	}
	return ""
}
