// Package connection is the node's transport: its connections to other nodes.
// A connection is config, a server.yaml `connections:` row. The transport
// dials each one, learns where it lands, and routes every id shaped
// "<conn>/<remote-id…>" to that connection's client, prepending the segment on
// the way back through rpc.TransitQualifyTiles, the same transit rule the node
// applies a level up. It is not a plugin and owns no tiles.
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
// config. Production is dial.Dial, whose ssh session is lazy and self-healing;
// tests inject in-process nodes.
type Dialer func(cfg dial.Config) (namespace.Namespace, func(), error)

// bootDialWait bounds how long ConnectAll waits for each connection at boot
// before serving anyway. The dial keeps trying in the background. A var so a
// test can wait it out; see bootwait_test.go.
var bootDialWait = 5 * time.Second

// learnRootWait bounds the Info that learns where a connection lands. A far
// node that accepts the dial and then never answers fails the learn here, with
// the reason on its row, rather than holding the caller forever. A var so a
// test can wait it out; see learnwait_test.go.
var learnRootWait = 15 * time.Second

// rowsHandshakeWait bounds the far Handshake a row's framing comes from. A far
// node that stopped answering costs its own row a viewport, never the + menu
// its answer. A var so a test can wait it out; see rowswait_test.go.
var rowsHandshakeWait = time.Second

// Server is the transport: a namespace.Namespace whose ids are chains through
// its connections, each itself a Namespace read off the far node's connection
// door.
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
	// one fact behind a pending row's status. Never persisted.
	rootErr map[string]string
	// dark is the detail of each connection's last down transition, by name,
	// absent while its event stream is up. Reachability is a state, not a
	// moment, so a subscriber attaching after the machine died is told from
	// here. One writer, noteHealth.
	dark map[string]string
	// mismatch is the landing verdict, by name: set when the far end answers a
	// home that is not the one this connection's stored references were
	// written against. It is a refusal, not a failure, so it lives apart from
	// rootErr and dark. One writer, learnRoot. Never persisted: the stored
	// landing is the fact, and this is what the last answer said about it.
	mismatch map[string]string

	hub *eventhub.Hub[*gridwellv1.Event]
}

// Conn is one declared connection with what the store remembers about it.
type Conn struct {
	Cfg        config.ConnectionConfig
	RemoteRoot string // the learned landing (the far node's home grid, in ITS frame); "" until learned
}

// liveConn is one connection's constructed transport. Constructing is cheap
// and non-blocking, because the ssh layer is lazy.
type liveConn struct {
	client namespace.Namespace
	closer func()
	cancel context.CancelFunc // stops the root-fetch/fan-in goroutines
	// rootFetching single-flights the remote-root learn.
	rootFetching bool
	// verified means this transport has said where it lands and it matches the
	// store. It rides the liveConn, so every fresh transport asks again: a
	// landing taken as settled forever is how a name silently starts serving
	// another node's tiles.
	verified bool
}

// Row is a connection as the handshake lists it; the node qualifies the uuid
// with its own id.
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
// connections. server.yaml is authoritative about what is declared and
// retired_names about what is retired.
//
// Retirement is explicit: a declared name that retired_names holds is refused,
// and a retired name is reserved forever. A stored name the config merely does
// not declare is left exactly as it is, so its mounts and links go dead by the
// boot roster (client/deadref) and come back with its stanza. Boot never
// retires a name on absence, and the `deleted` column is written from
// retired_names here and nowhere else.
//
// home is the host's home directory; "" means no ~ defaults, so keys must be
// explicit paths.
//
// It is also the boot gate on host-local config: every declared row must
// resolve to a dial plan whose files are there, so `serve` refuses to start
// rather than coming up with a connection that could never have dialed.
func New(db *DB, dialer Dialer, home string, conns []config.ConnectionConfig, retired []string) (*Server, error) {
	ctx := context.Background()
	s := &Server{db: db, dial: dialer, home: home, conns: map[string]*Conn{},
		live: map[string]*liveConn{}, rootErr: map[string]string{}, dark: map[string]string{},
		mismatch: map[string]string{}, hub: eventhub.New(rpc.EventKey)}
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
		// The host-local half of the row: facts this machine can settle, so a
		// connection that can never dial fails the boot instead of coming up
		// quietly dark. Whether the far node answers is deliberately not
		// asked here, since a laptop on a plane still serves its home.
		if _, err := s.dialConfig(c); err != nil {
			return nil, fmt.Errorf("connection %q: %w", c.Name, err)
		}
		row, err := db.Get(ctx, c.Name)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return nil, err
		}
		if err := db.Ensure(ctx, c.Name); err != nil {
			return nil, err
		}
		s.conns[c.Name] = &Conn{Cfg: c, RemoteRoot: row.RemoteRoot}
		s.order = append(s.order, c.Name)
	}
	// Sync the mirror both ways. A tombstone retired_names does not hold is a
	// leftover from the boot reconcile that retired on absence, so clear it
	// and the mounts through that name come back with its stanza.
	rows, err := db.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.Deleted && !retiredSet[r.Name] {
			log.Printf("gridwell: connection %q: clearing a tombstone the old boot reconcile wrote; retirement now lives in retired_names", r.Name)
			if err := db.Revive(ctx, r.Name); err != nil {
				return nil, fmt.Errorf("connection %q: clear stale tombstone: %w", r.Name, err)
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
// per connection by bootDialWait: a reachable connection is live with its root
// known before the node serves, and an unreachable one has its error on its
// row and keeps trying lazily on every read.
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

// Rows lists the declared connections for the handshake, in config order. A
// dark one contributes zeros.
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
		mismatch := s.mismatch[name]
		s.mu.Unlock()
		if mismatch != "" {
			// The landing verdict outranks the learned root: the row keeps the
			// landing its references name, and says why nothing answers.
			r.RootGridID = rpc.QualifyID(name, root)
			r.StatusDetail = mismatch
			out = append(out, r)
			continue
		}
		if root != "" {
			r.RootGridID = rpc.QualifyID(name, root)
			r.StatusDetail = ""
			if lc != nil {
				vctx, cancel := context.WithTimeout(ctx, rowsHandshakeWait)
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

// routePlaceholder stands in for the local half of an id when only the
// connection matters.
const routePlaceholder = "0"

// forward is a resolved hop: the connection's client plus its name for
// prepending response ids.
type forward struct {
	ns     string
	client namespace.Namespace
}

// route resolves the connection an id chains through: the first segment names
// it, and the rest is the far node's own id, forwarded verbatim. A tile
// segment in first position is malformed, because the transport owns no tiles
// of its own.
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
		// The row's tombstone mirrors retired_names, so a name merely no
		// longer declared falls through to plain not-found and comes back
		// when its stanza does.
		if row, err := s.db.Get(ctx, first); err == nil && row.Deleted {
			return nil, "", status.Errorf(codes.NotFound, "connection: connection %q was retired", first)
		}
		return nil, "", status.Errorf(codes.NotFound, "connection: no connection %q", first)
	}
	// A connection whose landing contradicts the stored one serves nothing:
	// these ids were written against the node that is no longer there.
	s.mu.Lock()
	mismatch := s.mismatch[first]
	s.mu.Unlock()
	if mismatch != "" {
		return nil, "", status.Error(codes.FailedPrecondition, "connection: "+mismatch)
	}
	lc, err := s.ensureLive(c)
	if err != nil {
		return nil, "", err
	}
	return &forward{ns: first, client: lc.client}, rest, nil
}

// dialConfig resolves a declared connection to a dial.Config, applying the
// host-side defaults: port 22; the key is the first of ~/.ssh/id_ed25519 and
// ~/.ssh/id_rsa that exists; known_hosts is ~/.ssh/known_hosts. addr is
// required, because the far node's connection-door socket lives under its
// home, which only the operator knows.
//
// It is the one owner of what a connection's fields mean, so it is also the
// one gate on them: the plan is checked and every host-local file it names
// must be readable. New calls it at boot, which is how a bad path fails
// `serve` instead of leaving the connection dark.
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
// whose open failure then names the expected default.
func firstExisting(paths ...string) string {
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return paths[0]
}

// ensureLive returns the connection's transport, constructing it on first use.
// New already refused a config-shaped problem at boot, so the same check here
// catches only what changed underneath a running node.
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
		// Record the bare dial error: the wrapper is routing noise to whoever
		// reads the row status.
		s.rootErr[name] = err.Error()
		return nil, status.Errorf(codes.Unavailable, "connection: connection %q: %v", name, err)
	}
	delete(s.rootErr, name) // transport constructed; the learn may still fail
	ctx, cancel := context.WithCancel(context.Background())
	lc := &liveConn{client: client, closer: closer, cancel: cancel}
	s.live[name] = lc
	// Remote change events flow from the moment the connection is live,
	// prefixed with its segment: the node's fan-in shape one level down.
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

// learnRoot dials the transport and asks the far node where this connection
// lands, once per live transport. A first answer is persisted and published as
// a health event so open clients re-list.
//
// Every later answer is checked against the stored landing rather than
// trusted. A connection name is bound to the node it landed on, because that
// is what every stored reference through it was written against, so a
// different node now leaves the stored landing alone and the connection
// refused until the operator retires the name or restores the target.
func (s *Server) learnRoot(c *Conn) (string, error) {
	name := c.Cfg.Name
	lc, err := s.ensureLive(c)
	if err != nil {
		return "", err // ensureLive recorded the detail already
	}
	s.mu.Lock()
	root, verified := c.RemoteRoot, lc.verified
	s.mu.Unlock()
	if verified {
		return root, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), learnRootWait)
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
	if root != "" && info.RootGridId != root {
		return "", s.noteLandingMismatch(name, root, info.RootGridId)
	}
	if root == "" {
		if err := s.db.SetRemoteRoot(ctx, name, info.RootGridId); err != nil {
			return "", err
		}
	}
	s.setRootErr(name, "")
	s.mu.Lock()
	lc.verified = true
	healed := s.mismatch[name] != ""
	delete(s.mismatch, name)
	c.RemoteRoot = info.RootGridId
	s.mu.Unlock()
	// A first landing and a restored one both change what the menu can show,
	// so both make open clients re-list; an unchanged one says nothing.
	if root == "" || healed {
		s.hub.Publish(healthEvent(name, true, ""))
	}
	return info.RootGridId, nil
}

// noteLandingMismatch records the landing verdict and returns the refusal every
// read through the connection gets. It is deliberately not marked verified, so
// the connection keeps asking and restoring the target needs no restart.
func (s *Server) noteLandingMismatch(name, stored, answered string) error {
	msg := fmt.Sprintf("connection %q now lands on a different node; stored references name the old one — retire the name or restore the target", name)
	s.mu.Lock()
	first := s.mismatch[name] == ""
	s.mismatch[name] = msg
	s.mu.Unlock()
	if first {
		log.Printf("gridwell: %s (stored landing %s, the far node answered %s)", msg, stored, answered)
	}
	return status.Error(codes.FailedPrecondition, "connection: "+msg)
}

// kickRootFetch learns and verifies a connection's landing in the background,
// single-flight per connection, and a no-op once this transport has answered.
// A remembered landing is not enough to skip it: a transport that has never
// been asked has an unchecked landing.
func (s *Server) kickRootFetch(c *Conn) {
	lc, err := s.ensureLive(c)
	if err != nil {
		return // recorded in rootErr; the row says why
	}
	s.mu.Lock()
	if lc.verified || lc.rootFetching {
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

// healthEvent is one connection's reachability on the wire.
func healthEvent(ns string, up bool, detail string) *gridwellv1.Event {
	return &gridwellv1.Event{Payload: &gridwellv1.Event_PluginHealth{PluginHealth: &gridwellv1.EventPluginHealth{
		PluginUuid: ns, Healthy: up, Detail: detail,
	}}}
}

// noteHealth records one connection's reachability and publishes the
// transition. The record is what a later subscriber is told and also what
// decides a transition, so a connection retrying every five seconds publishes
// once.
func (s *Server) noteHealth(ns string, up bool, detail string) {
	s.mu.Lock()
	if _, wasDark := s.dark[ns]; wasDark == !up {
		s.mu.Unlock()
		return
	}
	if up {
		delete(s.dark, ns)
	} else {
		s.dark[ns] = detail
	}
	s.mu.Unlock()
	s.hub.Publish(healthEvent(ns, up, detail))
}

// darkNow is every connection the fan-in currently cannot reach, in name
// order. Only the dark ones: healthy is what a subscriber assumes of a
// connection it has heard nothing about, and an up event means resync.
func (s *Server) darkNow() []*gridwellv1.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.dark))
	for name := range s.dark {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]*gridwellv1.Event, 0, len(names))
	for _, name := range names {
		out = append(out, healthEvent(name, false, s.dark[name]))
	}
	return out
}

// fanInRemote forwards a connection's remote change events, each id prefixed
// with the connection segment, and re-dials the stream through
// namespace.Refollow so a dropped one comes back. Never silently: each
// transition rides noteHealth, which is also what a later subscriber is told.
func (s *Server) fanInRemote(ctx context.Context, ns string, client namespace.Namespace) {
	namespace.Refollow{
		Label: "connection " + ns,
		Down:  func(detail string) { s.noteHealth(ns, false, detail) },
		Up:    func() { s.noteHealth(ns, true, "") },
		Attempt: func(ctx context.Context, established func()) error {
			return namespace.Follow(ctx, client, &gridwellv1.SubscribeRequest{},
				func(ev *gridwellv1.Event) error {
					s.hub.Publish(rpc.TransitQualifyEvent(ns, ev))
					return nil
				}, established)
		},
	}.Run(ctx)
}

// Handshake forwards a namespaced request through the named connection: peel
// the connection segment, forward the rest, and re-qualify the answer.
//
// With no namespace it answers for the transport itself. The transport owns no
// tiles, so what it declares is its connections, each with the landing it has
// learned, in the transport's own frame. Rows is the one owner of that fact,
// and it is the door anything fronting this namespace asks, the source cache's
// whole-source walk included.
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
	// A handshake names a connection, not a tile, so route gets the connection
	// segment plus a row-shaped placeholder to make a well-formed chain.
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
		// A connection that cannot be resolved is not gone; only a retired one
		// is. A name the config merely stopped declaring answers not-gone and
		// its links survive, because a failed read must never sweep a tile.
		if first, _, ok := rpc.SplitID(req.TileId); ok {
			if row, derr := s.db.Get(ctx, first); derr == nil && row.Deleted {
				return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_GONE}, nil
			}
		}
		return nil, err
	}
	return fw.client.Probe(ctx, &gridwellv1.ProbeRequest{TileId: local})
}

// SetFraming forwards the one framing write, routed on whichever target the
// request names.
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
		// see its own frame.
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
	// the request is the caller's own message (namespace.Namespace).
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

// Subscribe streams every connection's prefixed remote events and the
// connections' own health, opening with what is dark right now. A down
// transition fires once, and the fan-in outlives every client stream, so a
// client opening while the machine is already gone would otherwise never hear
// of the outage and the cache in front would serve a remembered grid as if it
// were live. Attach first, then read the record, so a transition racing this
// arrives on the stream rather than falling in the gap.
func (s *Server) Subscribe(ctx context.Context, _ *gridwellv1.SubscribeRequest, send func(*gridwellv1.Event) error) error {
	ch, cancel := s.hub.Subscribe()
	defer cancel()
	for _, ev := range s.darkNow() {
		if err := send(ev); err != nil {
			return err
		}
	}
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			// The hub hands the same event to every subscriber and nobody
			// mutates it; the router's qualification clones.
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
// world. A connection that errors or times out contributes nothing, loudly.
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

// stripPrefix removes "<ns>/" from an id qualified from this frame, leaving
// other ids untouched. rpc.ChainedThrough is the one owner of the question.
func stripPrefix(id, ns string) string {
	if rpc.ChainedThrough(id, ns) {
		return id[len(ns)+1:]
	}
	return id
}
