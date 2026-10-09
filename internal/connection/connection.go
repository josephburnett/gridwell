// Package connection is the node's transport: its connections to other nodes,
// each a server.yaml `connections:` row. It routes every id shaped
// "<conn>/<remote-id…>" to that connection's client and owns no tiles.
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
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/connection/dial"
	"github.com/josephburnett/gridwell/internal/eventhub"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/trace"
)

// Dialer builds a namespace over a remote node's export from a resolved
// config; production is dial.Dial, tests inject in-process nodes.
type Dialer func(cfg dial.Config) (namespace.Namespace, func(), error)

// bootDialWait bounds how long ConnectAll waits for each connection at boot.
// A var so a test can wait it out.
var bootDialWait = 5 * time.Second

// learnRootWait bounds the Info that learns where a connection lands, so a far
// node that never answers fails the learn with the reason on its row. A var so
// a test can wait it out.
var learnRootWait = 15 * time.Second

// rowsHandshakeWait bounds the far Handshake a row's framing comes from: a
// silent far node costs its row a viewport, never the + menu its answer.
var rowsHandshakeWait = time.Second

// Server is the transport: a namespace.Namespace whose ids are chains through
// its connections, each itself a Namespace read off the far node's connection
// door.
type Server struct {
	namespace.Unimplemented

	st   *store.Store
	dial Dialer
	home string // the host's home dir, for ~-relative key defaults

	// conns is the declared set, by name, in config order (order).
	conns map[string]*Conn
	order []string

	mu   sync.Mutex
	live map[string]*liveConn // by name
	// bg is every goroutine a live connection runs; Close waits on it, and
	// closed refuses a dial that would add to it afterwards.
	bg     sync.WaitGroup
	closed bool
	// health holds every connection the transport cannot reach, by name;
	// absent is reachable. Every kind of failure is this one fact, written
	// only by note. Never persisted.
	health map[string]connState
	// off reads which connections the user disabled, by name; the record is
	// plugin.Registry's (SwitchedOff). Nil disables none.
	off func(name string) bool

	hub *eventhub.Hub[*gridwellv1.Event]

	// far is each connection's share of the interest (interest.go, farOf),
	// its grids guarded by mu.
	far map[string]*farInterest
}

// connState is a connection's reachability as of the last answer.
type connState struct {
	up     bool
	detail string // why it is down; "" when up
	// mismatch is the landing refusal: the far end answered a home other than
	// the one this connection's stored references were written against.
	mismatch bool
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
	// ctx bounds every goroutine and learn this transport runs; cancel ends
	// them, and Server.bg is what Close waits on.
	ctx    context.Context
	cancel context.CancelFunc
	// rootFetching single-flights the remote-root learn.
	rootFetching bool
	// verified means this transport has said where it lands and it matches the
	// store. It rides the liveConn so every fresh transport asks again: a
	// landing settled forever is how a name starts serving another node's tiles.
	verified bool
}

// The router calls the transport as a Go value; the compiler says so.
var _ namespace.Namespace = (*Server)(nil)

// New builds the transport and reconciles the node store's connection rows
// against the declared connections. server.yaml is authoritative about what is
// declared and retired_names about what is retired; boot never retires a name
// on absence, and the `deleted` column is written from retired_names here and
// nowhere else. A boot that changes nothing writes nothing. It is also the boot
// gate on host-local config: a row that could never dial fails `serve`. home ""
// means no ~ defaults. Closing the transport leaves st open; the node owns it.
func New(st *store.Store, dialer Dialer, home string, conns []config.ConnectionConfig, retired []string) (*Server, error) {
	ctx := context.Background()
	s := &Server{st: st, dial: dialer, home: home, conns: map[string]*Conn{},
		live: map[string]*liveConn{}, health: map[string]connState{},
		hub: eventhub.New(rpc.EventKey)}
	retiredSet := map[string]bool{}
	for _, r := range retired {
		retiredSet[r] = true
	}
	for _, c := range conns {
		// Host-local facts only; whether the far node answers is deliberately
		// not asked, since a laptop on a plane still serves its home.
		if _, err := s.dialConfig(c); err != nil {
			return nil, fmt.Errorf("connection %q: %w", c.Name, err)
		}
		row, err := st.Connection(ctx, c.Name)
		if errors.Is(err, store.ErrNotFound) {
			err = st.DeclareConnection(ctx, c.Name)
		}
		if err != nil {
			return nil, err
		}
		s.conns[c.Name] = &Conn{Cfg: c, RemoteRoot: row.RemoteRoot}
		s.order = append(s.order, c.Name)
	}
	rows, err := st.Connections(ctx)
	if err != nil {
		return nil, err
	}
	deleted := map[string]bool{}
	for _, r := range rows {
		deleted[r.Name] = r.Deleted
		// A tombstone retired_names does not hold is a leftover from the old
		// boot reconcile that retired on absence, so clear it.
		if r.Deleted && !retiredSet[r.Name] {
			log.Printf("gridwell: connection %q: clearing a tombstone the old boot reconcile wrote; retirement now lives in retired_names", r.Name)
			if err := st.SetConnectionRetired(ctx, r.Name, false); err != nil {
				return nil, fmt.Errorf("connection %q: clear stale tombstone: %w", r.Name, err)
			}
		}
	}
	for _, name := range retired {
		if deleted[name] {
			continue
		}
		if err := st.SetConnectionRetired(ctx, name, true); err != nil {
			return nil, fmt.Errorf("reserve retired name %q: %w", name, err)
		}
	}
	return s, nil
}

// Close tears down every live connection and waits for the goroutines they
// ran: a learn or a fan-in must not outlive the server that started it.
func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	for name, lc := range s.live {
		lc.cancel()
		lc.closer()
		delete(s.live, name)
	}
	s.mu.Unlock()
	s.bg.Wait()
	return nil
}

// SwitchedOff hands the server its reader of the user's switch, before
// anything dials.
func (s *Server) SwitchedOff(off func(name string) bool) { s.off = off }

func (s *Server) switchedOff(name string) bool { return s.off != nil && s.off(name) }

// Disable is a connection's switch (plugin.Registry.Switch), run once the
// registry records it off: its transport closes, cancelling everything it
// runs, and every later read through the name is refused unavailable without
// a dial, so the cache answers for it as for any dark connection.
func (s *Server) Disable(name string) {
	s.mu.Lock()
	lc := s.live[name]
	delete(s.live, name)
	s.mu.Unlock()
	if lc != nil {
		lc.cancel()
		lc.closer()
	}
	s.note(name, connState{detail: rpc.DisabledDetail})
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
				s.mu.Lock()
				root := c.RemoteRoot
				s.mu.Unlock()
				log.Printf("gridwell: connection %q (%s): connected — root %s", c.Cfg.Label, name, root)
			}
		case <-time.After(bootDialWait):
			log.Printf("gridwell: connection %q (%s): no answer after %v — still trying in the background", c.Cfg.Label, name, bootDialWait)
		case <-ctx.Done():
			return
		}
	}
}

// Rows lists the declared connections as the handshake answers them, in
// config order (rpc.ConnectionRow). A dark one contributes zero framing, which
// the source cache reads as silence (sourcecache.keepFraming).
func (s *Server) Rows(ctx context.Context) []*gridwellv1.PluginInfo {
	out := make([]*gridwellv1.PluginInfo, 0, len(s.order))
	for _, name := range s.order {
		c := s.conns[name]
		s.kickRootFetch(c)
		label := c.Cfg.Label
		if label == "" {
			label = name
		}
		s.mu.Lock()
		root := c.RemoteRoot
		lc := s.live[name]
		s.mu.Unlock()
		st := s.stateOf(name)
		status := st.detail
		var rootGridID string
		var view rpc.View
		switch {
		case st.mismatch:
			// The landing verdict outranks the learned root: the row keeps the
			// landing its references name, and says why nothing answers.
			rootGridID = rpc.QualifyID(name, root)
		case root != "":
			rootGridID, status = rpc.QualifyID(name, root), ""
			if lc != nil {
				vctx, cancel := context.WithTimeout(ctx, rowsHandshakeWait)
				if lp, err := lc.client.Handshake(vctx, &gridwellv1.HandshakeRequest{}); err == nil {
					if h := rpc.HomeRow(lp); h != nil {
						view = rpc.ViewOf(h.RootViewCx, h.RootViewCy, h.RootViewZoom)
					}
				}
				cancel()
			}
		}
		out = append(out, rpc.ConnectionRow(name, label, rootGridID, status, view))
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
// it and the rest is forwarded verbatim. The transport owns no tiles, so a tile
// segment in first position is malformed.
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
		// Only the tombstone's wording says forever; a name merely no
		// longer declared comes back with its stanza.
		if row, err := s.st.Connection(ctx, first); err == nil && row.Deleted {
			return nil, "", gwerr.DeadRef(first, "connection: connection %q was retired", first)
		}
		return nil, "", gwerr.DeadRef(first, "connection: no connection %q", first)
	}
	// A connection whose landing contradicts the stored one serves nothing:
	// these ids were written against the node that is no longer there.
	if st := s.stateOf(first); st.mismatch {
		return nil, "", status.Error(codes.FailedPrecondition, "connection: "+st.detail)
	}
	lc, err := s.ensureLive(c)
	if err != nil {
		return nil, "", err
	}
	return &forward{ns: first, client: lc.client}, rest, nil
}

// dialConfig resolves a declared connection to a dial.Config with host-side
// defaults: port 22, key ~/.ssh/id_ed25519 then id_rsa, ~/.ssh/known_hosts.
// It is the one owner of what a connection's fields mean, so it is also the
// one gate on them; addr is required because only the operator knows where
// the far node's socket lives.
func (s *Server) dialConfig(c config.ConnectionConfig) (dial.Config, error) {
	cfg := dial.Config{
		User:       c.User,
		KeyPath:    config.ExpandHome(c.Key, s.home),
		KnownHosts: config.ExpandHome(c.KnownHosts, s.home),
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
	// The dial runs under the lock so two readers cannot build two
	// transports; note takes the lock too, so every failure unlocks first.
	s.mu.Lock()
	if lc, ok := s.live[name]; ok {
		s.mu.Unlock()
		return lc, nil
	}
	if s.closed {
		s.mu.Unlock()
		return nil, status.Errorf(codes.Unavailable, "connection: connection %q: the node is shutting down", name)
	}
	if s.switchedOff(name) {
		s.mu.Unlock()
		return nil, status.Errorf(codes.Unavailable, "connection: connection %q: %s", name, rpc.DisabledDetail)
	}
	kv := map[string]string{"conn": name}
	trace.Emit("connection", "dial", "dial", kv)
	cfg, err := s.dialConfig(c.Cfg)
	if err == nil && s.dial == nil {
		err = errors.New("no dialer")
	}
	if err != nil {
		s.mu.Unlock()
		trace.Emit("connection", "dial", "dial failed: "+err.Error(), kv)
		s.note(name, connState{detail: err.Error()})
		return nil, status.Errorf(codes.FailedPrecondition, "connection: connection %q: %v", name, err)
	}
	client, closer, err := s.dial(cfg)
	if err != nil {
		s.mu.Unlock()
		trace.Emit("connection", "dial", "dial failed: "+err.Error(), kv)
		s.note(name, connState{detail: err.Error()})
		return nil, status.Errorf(codes.Unavailable, "connection: connection %q: %v", name, err)
	}
	trace.Emit("connection", "dial", "dial ok", kv)
	ctx, cancel := context.WithCancel(context.Background())
	lc := &liveConn{client: client, closer: closer, ctx: ctx, cancel: cancel}
	s.live[name] = lc
	// Counted under the lock, so a Close that found the map empty cannot miss
	// a goroutine, and one that found lc has cancelled it.
	s.bg.Add(2)
	s.mu.Unlock()
	// Remote change events flow, prefixed, from the moment the connection is live.
	go func() { defer s.bg.Done(); s.fanInRemote(ctx, name, client) }()
	go func() { defer s.bg.Done(); s.tellFar(ctx, name, client) }()
	return lc, nil
}

// learnRoot dials the transport and asks the far node where this connection
// lands, once per live transport. Every later answer is checked against the
// stored landing, not trusted: a name is bound to the node every stored
// reference through it was written against.
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
	ctx, cancel := context.WithTimeout(lc.ctx, learnRootWait)
	defer cancel()
	info, err := lc.client.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		s.note(name, connState{detail: status.Convert(err).Message()})
		return "", err
	}
	if info.RootGridId == "" {
		err := errors.New("the connected node declared no home")
		s.note(name, connState{detail: err.Error()})
		return "", err
	}
	if root != "" && info.RootGridId != root {
		return "", s.noteLandingMismatch(name, root, info.RootGridId)
	}
	if root == "" {
		if err := s.st.SetConnectionRoot(ctx, name, info.RootGridId); err != nil {
			// kickRootFetch drops this error, so the row is the only place
			// that can say why.
			s.note(name, connState{detail: err.Error()})
			return "", err
		}
	}
	s.mu.Lock()
	lc.verified = true
	c.RemoteRoot = info.RootGridId
	s.mu.Unlock()
	// A restored landing is a transition and note publishes it. A first one is
	// not, and it still changes what the menu can show, so open clients re-list.
	if !s.note(name, connState{up: true}) && root == "" {
		s.hub.Publish(rpc.HealthEvent(name, true, ""))
	}
	return info.RootGridId, nil
}

// noteLandingMismatch records the landing verdict and returns the refusal every
// read through the connection gets. It is deliberately not marked verified, so
// the connection keeps asking and restoring the target needs no restart.
func (s *Server) noteLandingMismatch(name, stored, answered string) error {
	msg := fmt.Sprintf("connection %q now lands on a different node; stored references name the old one — retire the name or restore the target", name)
	if s.note(name, connState{detail: msg, mismatch: true}) {
		log.Printf("gridwell: %s (stored landing %s, the far node answered %s)", msg, stored, answered)
	}
	return status.Error(codes.FailedPrecondition, "connection: "+msg)
}

// kickRootFetch learns and verifies a connection's landing in the background,
// single-flight, until this transport has answered; a remembered landing on an
// unasked transport is unchecked.
func (s *Server) kickRootFetch(c *Conn) {
	lc, err := s.ensureLive(c)
	if err != nil {
		return // recorded in health; the row says why
	}
	s.mu.Lock()
	if lc.verified || lc.rootFetching {
		s.mu.Unlock()
		return
	}
	lc.rootFetching = true
	s.bg.Add(1)
	s.mu.Unlock()
	go func() {
		defer s.bg.Done()
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

// note is the one writer of s.health: it records what the transport knows and
// publishes only a transition, answering whether it did, so a connection
// retrying every five seconds publishes once.
func (s *Server) note(name string, st connState) bool {
	s.mu.Lock()
	if s.switchedOff(name) {
		// A learn or fan-in that outlived Disable does not speak for it.
		st = connState{detail: rpc.DisabledDetail}
	}
	prev, known := s.health[name]
	if !known {
		prev = connState{up: true}
	}
	if st.up {
		delete(s.health, name)
	} else {
		s.health[name] = st
	}
	s.mu.Unlock()
	if prev.up == st.up && prev.mismatch == st.mismatch {
		return false
	}
	msg := "up"
	if !st.up {
		msg = "down: " + st.detail
	}
	trace.Emit("connection", "health", msg, map[string]string{"conn": name})
	s.hub.Publish(rpc.HealthEvent(name, st.up, st.detail))
	return true
}

// stateOf is what the transport last knew about one connection; nothing
// recorded is reachable.
func (s *Server) stateOf(name string) connState {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.health[name]; ok {
		return st
	}
	return connState{up: true}
}

// darkNow is every connection the transport cannot reach, in name order.
func (s *Server) darkNow() []*gridwellv1.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.health))
	for name := range s.health {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]*gridwellv1.Event, 0, len(names))
	for _, name := range names {
		out = append(out, rpc.HealthEvent(name, false, s.health[name].detail))
	}
	return out
}

// fanInRemote forwards a connection's remote change events, prefixed, and
// re-dials the stream through namespace.Refollow; each transition rides note.
func (s *Server) fanInRemote(ctx context.Context, ns string, client namespace.Namespace) {
	namespace.Refollow{
		Label: "connection " + ns,
		Down:  func(detail string) { s.note(ns, connState{detail: detail}) },
		Up:    func() { s.note(ns, connState{up: true}) },
		Attempt: func(ctx context.Context, established func()) error {
			// The far node forgot this session's interest when the last
			// stream closed; see tellFar.
			s.farOf(ns).poke()
			return namespace.Follow(ctx, client, &gridwellv1.SubscribeRequest{Session: s.farOf(ns).session},
				func(ev *gridwellv1.Event) error {
					s.hub.Publish(rpc.TransitQualifyEvent(ns, ev))
					return nil
				}, established)
		},
	}.Run(ctx)
}

// Handshake forwards a namespaced request through the named connection. With
// no namespace it answers for the transport itself: its connections, which
// Rows owns.
func (s *Server) Handshake(ctx context.Context, req *gridwellv1.HandshakeRequest) (*gridwellv1.HandshakeResponse, error) {
	ns := req.GetNamespace()
	if ns == "" {
		return &gridwellv1.HandshakeResponse{Plugins: s.Rows(ctx)}, nil
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
			if row, derr := s.st.Connection(ctx, first); derr == nil && row.Deleted {
				return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_GONE}, nil
			}
		}
		return nil, err
	}
	return fw.client.Probe(ctx, &gridwellv1.ProbeRequest{TileId: local})
}

// forwardVerb is the shape every non-streaming verb takes: route the id, build
// the far node's request around the local half, call it, and qualify the answer
// back into this frame.
func forwardVerb[Req, Resp any](ctx context.Context, s *Server, ref string,
	build func(local string, hop rpc.Hop) Req,
	call func(namespace.Namespace, context.Context, Req) (Resp, error),
	qualify func(ns string, resp Resp) Resp,
) (Resp, error) {
	var zero Resp
	fw, local, err := s.route(ctx, ref)
	if err != nil {
		return zero, err
	}
	resp, err := call(fw.client, ctx, build(local, rpc.InboundHop(ref, "", true)))
	if err != nil {
		return zero, err
	}
	return qualify(fw.ns, resp), nil
}

// peeled is the build for a request carrying more than one id: every one
// crosses by rpc.PeelRequest, the far node being a transit namespace.
func peeled[Req proto.Message](req Req) func(string, rpc.Hop) Req {
	return func(_ string, hop rpc.Hop) Req { return rpc.PeelRequest(hop, req) }
}

// asIs is the qualifier for an answer carrying no id of the far node's.
func asIs[Resp any](_ string, resp Resp) Resp { return resp }

// SetFraming forwards the one framing write, routed on whichever target the
// request names.
func (s *Server) SetFraming(ctx context.Context, req *gridwellv1.SetFramingRequest) (*gridwellv1.SetFramingResponse, error) {
	ref := req.TileId
	if ref == "" {
		ref = req.RootGridId
	}
	return forwardVerb(ctx, s, ref, peeled(req), namespace.Namespace.SetFraming, asIs)
}

func (s *Server) GetGrid(ctx context.Context, req *gridwellv1.GetGridRequest) (*gridwellv1.GetGridResponse, error) {
	return forwardVerb(ctx, s, req.GridId, func(local string, _ rpc.Hop) *gridwellv1.GetGridRequest {
		return &gridwellv1.GetGridRequest{GridId: local}
	}, namespace.Namespace.GetGrid, prependGridResp)
}

func (s *Server) GetTile(ctx context.Context, req *gridwellv1.GetTileRequest) (*gridwellv1.TileResponse, error) {
	return forwardVerb(ctx, s, req.TileId, func(local string, _ rpc.Hop) *gridwellv1.GetTileRequest {
		return &gridwellv1.GetTileRequest{TileId: local}
	}, namespace.Namespace.GetTile, prependTileResp)
}

func (s *Server) GetTilePreview(ctx context.Context, req *gridwellv1.GetTilePreviewRequest) (*gridwellv1.GetTilePreviewResponse, error) {
	return forwardVerb(ctx, s, req.TileId, func(local string, _ rpc.Hop) *gridwellv1.GetTilePreviewRequest {
		return &gridwellv1.GetTilePreviewRequest{TileId: local}
	}, namespace.Namespace.GetTilePreview, asIs)
}

func (s *Server) ShellSessionAlive(ctx context.Context, req *gridwellv1.ShellSessionAliveRequest) (*gridwellv1.ShellSessionAliveResponse, error) {
	return forwardVerb(ctx, s, req.TileId, func(local string, _ rpc.Hop) *gridwellv1.ShellSessionAliveRequest {
		return &gridwellv1.ShellSessionAliveRequest{TileId: local}
	}, namespace.Namespace.ShellSessionAlive, asIs)
}

func (s *Server) CreateTile(ctx context.Context, req *gridwellv1.CreateTileRequest) (*gridwellv1.TileResponse, error) {
	return forwardVerb(ctx, s, req.GridId, peeled(req), namespace.Namespace.CreateTile, prependTileResp)
}

func (s *Server) SetTile(ctx context.Context, req *gridwellv1.SetTileRequest) (*gridwellv1.TileResponse, error) {
	return forwardVerb(ctx, s, req.TileId, peeled(req), namespace.Namespace.SetTile, prependTileResp)
}

func (s *Server) PlaceTile(ctx context.Context, req *gridwellv1.PlaceTileRequest) (*gridwellv1.TileResponse, error) {
	return forwardVerb(ctx, s, req.TileId, peeled(req), namespace.Namespace.PlaceTile, prependTileResp)
}

func (s *Server) CloneTile(ctx context.Context, req *gridwellv1.CloneTileRequest) (*gridwellv1.TileResponse, error) {
	return forwardVerb(ctx, s, req.TileId, peeled(req), namespace.Namespace.CloneTile, prependTileResp)
}

func (s *Server) DeleteTile(ctx context.Context, req *gridwellv1.DeleteTileRequest) (*gridwellv1.DeleteTileResponse, error) {
	return forwardVerb(ctx, s, req.TileId, func(local string, _ rpc.Hop) *gridwellv1.DeleteTileRequest {
		out := proto.Clone(req).(*gridwellv1.DeleteTileRequest)
		out.TileId = local
		return out
	}, namespace.Namespace.DeleteTile, asIs)
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

// Subscribe streams every connection's prefixed remote events and their
// health, opening with what is dark right now: a down transition fires once,
// so a late client would otherwise never hear of the outage. Attach first,
// then read the record, so a racing transition is not lost.
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

// Search forwards the one find verb. An id: query routes to its connection;
// free text fans out to live connections only, never dialing the world.
func (s *Server) Search(ctx context.Context, req *gridwellv1.SearchRequest) (*gridwellv1.SearchResponse, error) {
	if q := rpc.ParseSearchQuery(req.Query); q.ID != "" {
		return forwardVerb(ctx, s, q.ID, func(local string, _ rpc.Hop) *gridwellv1.SearchRequest {
			return &gridwellv1.SearchRequest{Query: "id:" + local, Limit: req.Limit}
		}, namespace.Namespace.Search, prependSearchResp)
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
		q := prependSearchResp(hp.ns, rpc.SearchHop(resp, err))
		out.Results = append(out.Results, q.Results...)
		out.Skipped = append(out.Skipped, q.Skipped...)
	}
	return out, nil
}

func prependSearchResp(ns string, resp *gridwellv1.SearchResponse) *gridwellv1.SearchResponse {
	return rpc.QualifySearchResponse(ns, resp, func(ts []*gridwellv1.Tile) []*gridwellv1.Tile {
		return rpc.TransitQualifyTiles(ns, ts)
	})
}

func prependGridResp(ns string, resp *gridwellv1.GetGridResponse) *gridwellv1.GetGridResponse {
	return &gridwellv1.GetGridResponse{
		// The one transit grid rule, shared with the node's hop.
		Grid:  rpc.TransitQualifyGrid(ns, resp.Grid),
		Tiles: rpc.TransitQualifyTiles(ns, resp.Tiles),
	}
}

func prependTileResp(ns string, resp *gridwellv1.TileResponse) *gridwellv1.TileResponse {
	t := resp.GetTile()
	if t == nil {
		return resp
	}
	return &gridwellv1.TileResponse{Tile: rpc.TransitQualifyTiles(ns, []*gridwellv1.Tile{t})[0]}
}
