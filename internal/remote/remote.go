package remote

import (
	"context"
	"errors"
	"io"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/remote/dial"
)

// connGridID is the plugin's connection-list grid. Since the #251 flip it
// is declared as the INSTANCE grid, not the root: a storage address the
// row synthesis reads, never a landing page. It keeps serving under the
// same id forever — connection rows present as menu rows of their own
// automatically, and any legacy exit-well link to the list still resolves.
const connGridID = "0"

// Dialer builds a client of a remote node's export from a resolved config.
// Production is dial.Dial (whose ssh session is itself lazy and
// self-healing); tests inject in-process remotes.
type Dialer func(cfg dial.Config) (gridwellv1.GridwellClient, func(), error)

// Server is the multi-connection ssh plugin: a GridwellServer whose root grid
// holds one connection well per remote node, and whose router peels the
// minted connection segment from chained ids exactly as a node peels a
// plugin segment. Everything below a connection forwards to that
// connection's dialed client with the segment prepended on the way back
// (rpc.TransitQualifyTiles — the one transit rule).
type Server struct {
	gridwellv1.UnimplementedGridwellServer

	db   *DB
	dial Dialer
	home string // plugin host's home dir, for ~-relative param defaults
	// configMode: the connection set is OWNED by server.yaml (v2 #269);
	// every connection mutation refuses (see sync.go).
	configMode bool

	mu   sync.Mutex
	live map[string]*liveConn // by ns
	// rootErr is a connection's LAST dial/root-fetch failure, by ns —
	// the one fact behind a pending row's status (surfaced as
	// Tile.status_detail while the well has no child). Written only by
	// ensureLive and the root-fetch goroutine, cleared on success and on
	// params change (dropLive); never persisted.
	rootErr map[string]string

	hub *eventHub
}

// liveConn is one connection's constructed transport. Constructing is cheap
// and non-blocking (sshdial's ssh layer is lazy); a liveConn exists as soon
// as the connection has valid params.
type liveConn struct {
	client gridwellv1.GridwellClient
	closer func()
	cancel context.CancelFunc // stops the root-fetch/fan-in goroutines
	// rootFetching single-flights the remote-root learn.
	rootFetching bool
	// homeChecked marks that this process already re-resolved a stored
	// node-grid root ("<rnode>/0") to the remote HOME — bounded to once
	// per connection per process so a remote with no rooted plugins
	// (home IS the node grid) doesn't refetch on every list read.
	homeChecked bool
}

// New builds the plugin server. home is the host's home directory ("" =
// no ~ defaults; params must carry explicit paths).
func New(db *DB, dial Dialer, home string) *Server {
	return &Server{db: db, dial: dial, home: home, live: map[string]*liveConn{},
		rootErr: map[string]string{}, hub: newEventHub()}
}

// Close tears down every live connection.
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ns, lc := range s.live {
		lc.cancel()
		lc.closer()
		delete(s.live, ns)
	}
}

// ── routing ──────────────────────────────────────────────────────────────────

// forward is a resolved chain hop: the connection's client plus its ns for
// prepending response ids.
type forward struct {
	ns     string
	client gridwellv1.GridwellClient
}

// route decides where an id goes. A bare id ("0", "5") is local. A chained
// id peels its first segment: non-numeric = a connection namespace (the same
// grammar rule the URL path uses — a minted short id can never be purely
// numeric); a numeric first segment is malformed (local tiles are leaves).
func (s *Server) route(ctx context.Context, id string) (*forward, string, error) {
	first, rest, ok := rpc.SplitID(id)
	if !ok {
		return nil, id, nil // local
	}
	if _, err := strconv.ParseInt(first, 10, 64); err == nil {
		return nil, "", status.Errorf(codes.InvalidArgument, "sshhost: id %q chains through a numeric segment", id)
	}
	c, err := s.db.GetByNS(ctx, first)
	if errors.Is(err, ErrNotFound) {
		return nil, "", status.Errorf(codes.NotFound, "sshhost: no connection %q", first)
	}
	if err != nil {
		return nil, "", err
	}
	if c.Deleted {
		return nil, "", status.Errorf(codes.NotFound, "sshhost: connection %q was deleted", first)
	}
	lc, err := s.ensureLive(c)
	if err != nil {
		return nil, "", err
	}
	return &forward{ns: first, client: lc.client}, rest, nil
}

// remoteHome resolves where a descent into this connection LANDS: the
// remote's HOME — the same rule a direct client's boot applies (first
// plugin with a root grid; the node grid as the fallback) — so entering
// a node through a mount and connecting to it directly land in the same
// place ("when I descend into a node, I am there", 2026-08-16). The
// node grid stays addressable; it just is not the landing page, same as
// locally (owner decision 2026-07-19).
func (s *Server) remoteHome(ctx context.Context, lc *liveConn) (string, error) {
	lp, err := lc.client.ListPlugins(ctx, &gridwellv1.ListPluginsRequest{})
	if err == nil {
		for _, p := range lp.Plugins {
			if p.RootGridId != "" {
				return p.RootGridId, nil
			}
		}
	}
	// No rooted plugin (or a pre-remote-menu node that doesn't serve the
	// list on its export): the node grid, from Info — the old behavior.
	info, ierr := lc.client.Info(ctx, &gridwellv1.InfoRequest{})
	if ierr != nil {
		return "", ierr
	}
	return info.RootGridId, nil
}

// ListPlugins forwards a NAMESPACED request through the named connection
// (remote-menu, 2026-08-16): peel the connection segment, forward the
// rest to its node export, and re-qualify the answer with the segment —
// the same hop rule as every routed read, so the + menu inside a remote
// pane shows THAT node's plugins with ids routable from here. A request
// with no namespace is refused: a parameterized transit plugin has no
// plugin list of its own.
func (s *Server) ListPlugins(ctx context.Context, req *gridwellv1.ListPluginsRequest) (*gridwellv1.ListPluginsResponse, error) {
	ns := req.GetNamespace()
	if ns == "" {
		return nil, status.Error(codes.InvalidArgument, "remote: ListPlugins needs a connection namespace")
	}
	first, rest, ok := rpc.SplitID(ns)
	if !ok {
		first, rest = ns, ""
	}
	fw, _, err := s.route(ctx, first+"/0")
	if err != nil {
		return nil, err
	}
	resp, err := fw.client.ListPlugins(ctx, &gridwellv1.ListPluginsRequest{Namespace: rest})
	if err != nil {
		return nil, err
	}
	return rpc.TransitQualifyPluginList(first, resp), nil
}

// ensureLive returns the connection's transport, constructing it on first
// use. Params must be committed and valid; a config-shaped problem (bad key
// path) surfaces here, loudly, on every attempt.
func (s *Server) ensureLive(c *Conn) (*liveConn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if lc, ok := s.live[c.NS]; ok {
		return lc, nil
	}
	if c.Params == "" {
		return nil, status.Errorf(codes.FailedPrecondition, "sshhost: connection %q has no parameters yet", c.NS)
	}
	p, err := ParseParams([]byte(c.Params))
	if err != nil {
		s.rootErr[c.NS] = err.Error()
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	cfg, err := p.DialConfig(s.home)
	if err != nil {
		s.rootErr[c.NS] = err.Error()
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	client, closer, err := s.dial(cfg)
	if err != nil {
		// Record the BARE dial error (the ns wrapper below is routing
		// noise to the person reading the row status).
		s.rootErr[c.NS] = err.Error()
		return nil, status.Errorf(codes.Unavailable, "sshhost: connection %q: %v", c.NS, err)
	}
	delete(s.rootErr, c.NS) // transport constructed; the learn may still fail
	ctx, cancel := context.WithCancel(context.Background())
	lc := &liveConn{client: client, closer: closer, cancel: cancel}
	s.live[c.NS] = lc
	// Remote change events flow from the moment the connection is live,
	// prefixed with its segment — the same fan-in shape the top-level server
	// runs per plugin, one level down.
	go s.fanInRemote(ctx, c.NS, client)
	return lc, nil
}

// dropLive tears down a connection's transport (params changed or deleted).
func (s *Server) dropLive(ns string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rootErr, ns) // stale trouble must not outlive the params that caused it
	if lc, ok := s.live[ns]; ok {
		lc.cancel()
		lc.closer()
		delete(s.live, ns)
	}
}

// setRootErr records ("" clears) a connection's last dial/root-fetch failure.
func (s *Server) setRootErr(ns, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if detail == "" {
		delete(s.rootErr, ns)
		return
	}
	s.rootErr[ns] = detail
}

// stampStatus writes the connection's recorded failure onto its well tile —
// only while the well is CHILDLESS: once the chain is learned, a transient
// outage is the mount-health story, not a pending-row status.
func (s *Server) stampStatus(c *Conn, t *gridwellv1.Tile) {
	if c.RemoteRoot != "" {
		return
	}
	s.mu.Lock()
	t.StatusDetail = s.rootErr[c.NS]
	s.mu.Unlock()
}

// kickRootFetch learns the remote's root grid id (its node grid) from its
// Info, in the background, single-flight per connection. Success backfills
// remote_root — the moment the connection well gains its child — and emits
// the change so open clients refresh.
func (s *Server) kickRootFetch(c *Conn) {
	if c.Params == "" || c.Deleted {
		return
	}
	// Fast path without dialing: a resolved home is final for this run
	// (learnRoot re-checks under its own rules — the node-grid re-resolve
	// and homeChecked live THERE, the one learn implementation).
	refreshNodeGridRoot := c.RemoteRoot != "" && strings.HasSuffix(c.RemoteRoot, "/0") &&
		strings.Count(c.RemoteRoot, "/") == 1
	if c.RemoteRoot != "" && !refreshNodeGridRoot {
		return
	}
	lc, err := s.ensureLive(c)
	if err != nil {
		// Params/dial problem — ensureLive just recorded it (rootErr), so
		// the well's status_detail says why; direct paths also error.
		return
	}
	s.mu.Lock()
	if lc.rootFetching || (refreshNodeGridRoot && lc.homeChecked) {
		s.mu.Unlock()
		return
	}
	lc.rootFetching = true
	s.mu.Unlock()
	conn := *c
	ns := c.NS
	go func() {
		defer func() {
			s.mu.Lock()
			if l, ok := s.live[ns]; ok {
				l.rootFetching = false
			}
			s.mu.Unlock()
		}()
		// Failure is already recorded (rootErr) by learnRoot/ensureLive so
		// the well's status_detail says why; retried on the next read.
		_, _ = s.learnRoot(&conn)
	}()
}

// fanInRemote forwards a connection's remote change events, each id prefixed
// with the connection segment. Plain retry loop: the transport underneath
// self-heals (sshdial's redialer), so a dropped stream just re-subscribes —
// but never silently: a connection whose events stop presents as "tiles
// stopped updating" with no evidence, so each transition is logged and
// published as an EventPluginHealth (the same contract the server's
// fanInEvents keeps for local plugins, issue #47).
func (s *Server) fanInRemote(ctx context.Context, ns string, client gridwellv1.GridwellClient) {
	healthy := true
	report := func(up bool, detail string) {
		s.hub.publish(&gridwellv1.Event{Payload: &gridwellv1.Event_PluginHealth{PluginHealth: &gridwellv1.EventPluginHealth{
			PluginUuid: ns, Healthy: up, Detail: detail,
		}}})
	}
	for {
		stream, err := client.Subscribe(ctx, &gridwellv1.SubscribeRequest{})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("gridwell: remote %s: event subscribe failed: %v (retrying in 5s)", ns, err)
			if healthy {
				healthy = false
				report(false, err.Error())
			}
		} else {
			if !healthy {
				healthy = true
				report(true, "")
			}
			for {
				ev, rerr := stream.Recv()
				if rerr != nil {
					if ctx.Err() != nil {
						return
					}
					log.Printf("gridwell: remote %s: event stream ended: %v (retrying in 5s)", ns, rerr)
					if healthy {
						healthy = false
						report(false, rerr.Error())
					}
					break
				}
				s.hub.publish(rpc.TransitQualifyEvent(ns, ev))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// ── the connection well ──────────────────────────────────────────────────────

// tileFromConn renders a connection row as its well tile. Reference is set —
// dashed border, delete unlinks, the child is shared not owned — and the
// child appears only once the remote's root is known.
func tileFromConn(c *Conn) *gridwellv1.Tile {
	alt := c.AltText
	if !c.AltUser {
		if l := autoLabel(c.Params); l != "" {
			alt = l
		}
	}
	t := &gridwellv1.Tile{
		Id:        strconv.FormatInt(c.ID, 10),
		GridId:    connGridID,
		ObjectId:  c.ObjectID,
		Kind:      "well",
		X:         c.X,
		Y:         c.Y,
		W:         c.W,
		H:         c.H,
		AltText:   alt,
		Version:   c.Version,
		ViewX:     c.ViewX,
		ViewY:     c.ViewY,
		ViewZoom:  c.ViewZoom,
		Reference: true,
	}
	if c.RemoteRoot != "" {
		t.ChildGridId = rpc.QualifyID(c.NS, c.RemoteRoot)
	}
	return t
}

func dbErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrVersionConflict):
		return status.Error(codes.FailedPrecondition, err.Error())
	default:
		return err
	}
}

// ── lifecycle ────────────────────────────────────────────────────────────────

func (s *Server) Info(ctx context.Context, _ *gridwellv1.InfoRequest) (*gridwellv1.InfoResponse, error) {
	resp := &gridwellv1.InfoResponse{
		// The DECLARATIONS the host reads instead of knowing this kind:
		// transit (ids are chains from another node) and the generic globe
		// glyph (empty = globe fallback).
		Transit:     true,
		Kind:        "remote",
		DisplayName: "connections",
		// The #251 flip: no root grid — the connection list is the INSTANCE
		// grid, and the server synthesizes one menu row per connection
		// from it. Writable is FALSE deliberately: it is the "+ palette
		// shows here" gate. No root grid means no root view to report.
		InstanceGridId: connGridID,
		Watch:          true,
	}
	// No creation schema, ever: the instance picker retired with the v2
	// config-managed connections (2026-08-23) — a connection is a menu
	// row, and the list is edited in server.yaml.
	return resp, nil
}

func (s *Server) Probe(ctx context.Context, req *gridwellv1.ProbeRequest) (*gridwellv1.ProbeResponse, error) {
	fw, local, err := s.route(ctx, req.TileId)
	if err != nil {
		// A connection that cannot be resolved is NOT gone — only a
		// tombstone is. A failed read must never sweep a tile.
		if first, _, ok := rpc.SplitID(req.TileId); ok {
			if c, derr := s.db.GetByNS(ctx, first); derr == nil && c.Deleted {
				return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_GONE}, nil
			}
		}
		return nil, err
	}
	if fw != nil {
		return fw.client.Probe(ctx, &gridwellv1.ProbeRequest{TileId: local})
	}
	id, err := strconv.ParseInt(local, 10, 64)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "sshhost: bad tile id %q", local)
	}
	c, err := s.db.Get(ctx, id)
	if errors.Is(err, ErrNotFound) || (err == nil && c.Deleted) {
		return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_GONE}, nil
	}
	if err != nil {
		return nil, err
	}
	return &gridwellv1.ProbeResponse{Presence: gridwellv1.ProbeResponse_PRESENCE_PRESENT}, nil
}

func (s *Server) SetRootView(ctx context.Context, req *gridwellv1.SetRootViewRequest) (*gridwellv1.SetRootViewResponse, error) {
	fw, local, err := s.route(ctx, req.RootGridId)
	if err != nil {
		return nil, err
	}
	if fw != nil {
		// The CONNECTION'S OWN ROOT: the connection row is the DOOR, and
		// the door owns its viewport (#263's rule; v2 #269 — the menu
		// row's root_view reads the row's view_*). Persist framing HERE,
		// never forward: forwarding wrote the FAR node's landing framing
		// (clobbering what its own clients left) while the local row
		// stayed at zero — the ascent wrote one place, the re-entry read
		// another, and the round trip silently lost the viewport.
		if c, cerr := s.db.GetByNS(ctx, fw.ns); cerr == nil && !c.Deleted && c.RemoteRoot == local {
			if _, ferr := s.db.SetFraming(ctx, c.ID, int64(req.Cx), int64(req.Cy), req.Zoom); ferr != nil {
				return nil, dbErr(ferr)
			}
			return &gridwellv1.SetRootViewResponse{}, nil
		}
		// Deeper targets (a far PLUGIN's root through the routed menu)
		// forward: that root belongs to the far node.
		out := *req
		out.RootGridId = local
		return fw.client.SetRootView(ctx, &out)
	}
	// Local: the plugin has no root grid (#251 — the connection list is an
	// instance grid, never a landing page), so there is no root view to
	// persist. The transit branch above still forwards root-view writes to
	// remote plugins reached through a connection.
	return nil, status.Error(codes.Unimplemented, "sshhost: no root grid (parameterized plugin)")
}

// ── reads ────────────────────────────────────────────────────────────────────

func (s *Server) GetGrid(ctx context.Context, req *gridwellv1.GetGridRequest) (*gridwellv1.GetGridResponse, error) {
	fw, local, err := s.route(ctx, req.GridId)
	if err != nil {
		return nil, err
	}
	if fw != nil {
		resp, err := fw.client.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: local})
		if err != nil {
			return nil, err
		}
		return &gridwellv1.GetGridResponse{
			// The one transit grid rule, shared with the server's hop.
			Grid:  rpc.TransitQualifyGrid(fw.ns, resp.Grid),
			Tiles: rpc.TransitQualifyTiles(fw.ns, resp.Tiles),
		}, nil
	}
	if local != connGridID {
		return nil, status.Errorf(codes.NotFound, "sshhost: no grid %q", local)
	}
	conns, err := s.db.List(ctx)
	if err != nil {
		return nil, err
	}
	gv, err := s.db.GridVersion(ctx)
	if err != nil {
		return nil, err
	}
	tiles := make([]*gridwellv1.Tile, 0, len(conns))
	for _, c := range conns {
		// A connection that has params but no learned root retries the learn
		// on every list — the read is what shows the child, so the read is
		// what re-kicks a remote that was down. Kick BEFORE rendering the
		// row: a config-shaped failure records synchronously, so the same
		// read that retries also says why it keeps failing.
		s.kickRootFetch(c)
		t := tileFromConn(c)
		s.stampStatus(c, t)
		tiles = append(tiles, t)
	}
	grid := &gridwellv1.Grid{
		Id:      connGridID,
		Version: gv,
		// NOT writable: that's the "+ palette shows here" gate (#251).
		// The plugin stamps its own grid because it is registered
		// TRANSIT — the server forwards this stamp verbatim.
	}
	return &gridwellv1.GetGridResponse{
		Grid:  grid,
		Tiles: tiles,
	}, nil
}

// localConn parses a bare local tile id and loads its live row.
func (s *Server) localConn(ctx context.Context, local string) (*Conn, error) {
	id, err := strconv.ParseInt(local, 10, 64)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "sshhost: bad tile id %q", local)
	}
	c, err := s.db.Get(ctx, id)
	if err != nil {
		return nil, dbErr(err)
	}
	if c.Deleted {
		return nil, status.Errorf(codes.NotFound, "sshhost: tile %s was deleted", local)
	}
	return c, nil
}

func (s *Server) GetTile(ctx context.Context, req *gridwellv1.GetTileRequest) (*gridwellv1.TileResponse, error) {
	fw, local, err := s.route(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	if fw != nil {
		resp, err := fw.client.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: local})
		if err != nil {
			return nil, err
		}
		return prependTileResp(fw.ns, resp), nil
	}
	c, err := s.localConn(ctx, local)
	if err != nil {
		return nil, err
	}
	t := tileFromConn(c)
	// The picker's create flow polls THIS read while the row says
	// "connecting…" — it must carry the recorded failure, not hide it.
	s.stampStatus(c, t)
	return &gridwellv1.TileResponse{Tile: t}, nil
}

func (s *Server) GetTilePreview(ctx context.Context, req *gridwellv1.GetTilePreviewRequest) (*gridwellv1.GetTilePreviewResponse, error) {
	fw, local, err := s.route(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	if fw != nil {
		return fw.client.GetTilePreview(ctx, &gridwellv1.GetTilePreviewRequest{TileId: local})
	}
	return nil, status.Error(codes.NotFound, "sshhost: a connection well has no stored preview")
}

func (s *Server) ShellSessionAlive(ctx context.Context, req *gridwellv1.ShellSessionAliveRequest) (*gridwellv1.ShellSessionAliveResponse, error) {
	fw, local, err := s.route(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	if fw != nil {
		return fw.client.ShellSessionAlive(ctx, &gridwellv1.ShellSessionAliveRequest{TileId: local})
	}
	return &gridwellv1.ShellSessionAliveResponse{Alive: false}, nil
}

// ── mutations ────────────────────────────────────────────────────────────────

func (s *Server) CreateTile(ctx context.Context, req *gridwellv1.CreateTileRequest) (*gridwellv1.TileResponse, error) {
	fw, local, err := s.route(ctx, req.GridId)
	if err != nil {
		return nil, err
	}
	if fw != nil {
		out := *req
		out.GridId = local
		if req.Tile != nil {
			t := *req.Tile
			// A qualified child/target crossing INTO the connection was
			// qualified from OUR side; strip our segment so the remote sees
			// its own frame. (The server-side link machinery does the same
			// strip one level up.)
			t.ChildGridId = stripPrefix(t.ChildGridId, fw.ns)
			t.LinkTargetId = stripPrefix(t.LinkTargetId, fw.ns)
			out.Tile = &t
		}
		resp, err := fw.client.CreateTile(ctx, &out)
		if err != nil {
			return nil, err
		}
		return prependTileResp(fw.ns, resp), nil
	}
	if local != connGridID {
		return nil, status.Errorf(codes.NotFound, "sshhost: no grid %q", local)
	}
	if s.configMode {
		return nil, status.Error(codes.FailedPrecondition, errConfigMode.Error())
	}
	t := req.Tile
	if t == nil {
		return nil, status.Error(codes.InvalidArgument, "sshhost: nil tile")
	}
	if t.Kind != "well" {
		return nil, status.Errorf(codes.InvalidArgument,
			"sshhost: the ssh plugin hosts connections — drop a well (got kind %q)", t.Kind)
	}
	w, h := t.W, t.H
	if w <= 0 {
		w = 1
	}
	if h <= 0 {
		h = 1
	}
	if err := s.checkOverlap(ctx, t.X, t.Y, w, h, 0); err != nil {
		return nil, err
	}
	c, err := s.db.Create(ctx, t.X, t.Y, w, h, t.AltText)
	if err != nil {
		return nil, err
	}
	_ = s.db.BumpGridVersion(ctx)
	tile := tileFromConn(c)
	s.hub.publish(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{
		TileChanged: &gridwellv1.TileChanged{Tile: tile}}})
	return &gridwellv1.TileResponse{Tile: tile}, nil
}

// checkOverlap refuses a placement that intersects another live connection.
func (s *Server) checkOverlap(ctx context.Context, x, y, w, h, exclude int64) error {
	conns, err := s.db.List(ctx)
	if err != nil {
		return err
	}
	for _, c := range conns {
		if c.ID == exclude {
			continue
		}
		if x < c.X+c.W && c.X < x+w && y < c.Y+c.H && c.Y < y+h {
			return status.Errorf(codes.FailedPrecondition,
				"sshhost: placement overlaps connection %d", c.ID)
		}
	}
	return nil
}

func (s *Server) SetTile(ctx context.Context, req *gridwellv1.SetTileRequest) (*gridwellv1.TileResponse, error) {
	fw, local, err := s.route(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	if fw != nil {
		out := *req
		out.TileId = local
		resp, err := fw.client.SetTile(ctx, &out)
		if err != nil {
			return nil, err
		}
		return prependTileResp(fw.ns, resp), nil
	}
	c, err := s.localConn(ctx, local)
	if err != nil {
		return nil, err
	}
	switch {
	case req.Rename != "":
		if s.configMode {
			return nil, status.Error(codes.FailedPrecondition, errConfigMode.Error())
		}
		c, err = s.db.Rename(ctx, c.ID, req.Version, req.Rename)
	case req.ContentZoom != nil:
		return nil, status.Error(codes.InvalidArgument, "sshhost: content_zoom is refused for wells")
	case req.Tile != nil && req.Tile.Kind == "well":
		// Framing-class: never bumps (face #3 of the primary rule).
		c, err = s.db.SetFraming(ctx, c.ID, req.Tile.ViewX, req.Tile.ViewY, req.Tile.ViewZoom)
	default:
		return nil, status.Error(codes.InvalidArgument, "sshhost: unsupported SetTile operation for a connection well")
	}
	if err != nil {
		return nil, dbErr(err)
	}
	tile := tileFromConn(c)
	s.hub.publish(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{
		TileChanged: &gridwellv1.TileChanged{Tile: tile}}})
	return &gridwellv1.TileResponse{Tile: tile}, nil
}

func (s *Server) PlaceTile(ctx context.Context, req *gridwellv1.PlaceTileRequest) (*gridwellv1.TileResponse, error) {
	fwTile, localTile, err := s.route(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	fwGrid, localGrid, err := s.route(ctx, req.GridId)
	if err != nil {
		return nil, err
	}
	if nsOf(fwTile) != nsOf(fwGrid) {
		return nil, status.Error(codes.Unimplemented,
			"sshhost: placement never crosses a connection boundary — clone or link instead")
	}
	if fwTile != nil {
		out := *req
		out.TileId = localTile
		out.GridId = localGrid
		resp, err := fwTile.client.PlaceTile(ctx, &out)
		if err != nil {
			return nil, err
		}
		return prependTileResp(fwTile.ns, resp), nil
	}
	if localGrid != connGridID {
		return nil, status.Errorf(codes.NotFound, "sshhost: no grid %q", localGrid)
	}
	c, err := s.localConn(ctx, localTile)
	if err != nil {
		return nil, err
	}
	if req.W <= 0 || req.H <= 0 {
		return nil, status.Error(codes.InvalidArgument, "sshhost: w and h must be positive")
	}
	if err := s.checkOverlap(ctx, req.X, req.Y, req.W, req.H, c.ID); err != nil {
		return nil, err
	}
	c, err = s.db.SetPlacement(ctx, c.ID, req.Version, req.X, req.Y, req.W, req.H)
	if err != nil {
		return nil, dbErr(err)
	}
	tile := tileFromConn(c)
	s.hub.publish(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{
		TileChanged: &gridwellv1.TileChanged{Tile: tile}}})
	return &gridwellv1.TileResponse{Tile: tile}, nil
}

func (s *Server) CloneTile(ctx context.Context, req *gridwellv1.CloneTileRequest) (*gridwellv1.TileResponse, error) {
	fwTile, localTile, err := s.route(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	fwGrid, localGrid, err := s.route(ctx, req.DestGridId)
	if err != nil {
		return nil, err
	}
	if nsOf(fwTile) != nsOf(fwGrid) {
		return nil, status.Error(codes.Unimplemented,
			"sshhost: clone never crosses a connection boundary here — the server's cross-plugin copy handles that")
	}
	if fwTile != nil {
		out := *req
		out.TileId = localTile
		out.DestGridId = localGrid
		resp, err := fwTile.client.CloneTile(ctx, &out)
		if err != nil {
			return nil, err
		}
		return prependTileResp(fwTile.ns, resp), nil
	}
	// Clone of a connection well: a NEW connection to the same endpoint —
	// same params, its own minted namespace, its own dial. (The localdb
	// discipline: clone copies the row; here the "content" is the params.)
	src, err := s.localConn(ctx, localTile)
	if err != nil {
		return nil, err
	}
	if src.Version != req.Version {
		return nil, status.Errorf(codes.FailedPrecondition,
			"sshhost: tile %s is at version %d, claim was %d", localTile, src.Version, req.Version)
	}
	if err := s.checkOverlap(ctx, req.X, req.Y, src.W, src.H, 0); err != nil {
		return nil, err
	}
	c, err := s.db.Create(ctx, req.X, req.Y, src.W, src.H, src.AltText)
	if err != nil {
		return nil, err
	}
	if src.Params != "" {
		if c, err = s.db.SetParams(ctx, c.ID, c.Version, src.Params); err != nil {
			return nil, dbErr(err)
		}
		s.kickRootFetch(c)
	}
	_ = s.db.BumpGridVersion(ctx)
	tile := tileFromConn(c)
	s.hub.publish(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{
		TileChanged: &gridwellv1.TileChanged{Tile: tile}}})
	return &gridwellv1.TileResponse{Tile: tile}, nil
}

func (s *Server) DeleteTile(ctx context.Context, req *gridwellv1.DeleteTileRequest) (*gridwellv1.DeleteTileResponse, error) {
	fw, local, err := s.route(ctx, req.TileId)
	if err != nil {
		return nil, err
	}
	if fw != nil {
		return fw.client.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: local, Version: req.Version})
	}
	c, err := s.localConn(ctx, local)
	if err != nil {
		return nil, err
	}
	if s.configMode {
		return nil, status.Error(codes.FailedPrecondition, errConfigMode.Error())
	}
	// Unlink, never cascade: the remote is untouched; the minted namespace
	// stays reserved forever (tombstone, not DELETE).
	if err := s.db.Tombstone(ctx, c.ID, req.Version); err != nil {
		return nil, dbErr(err)
	}
	s.dropLive(c.NS)
	_ = s.db.BumpGridVersion(ctx)
	s.hub.publish(&gridwellv1.Event{Payload: &gridwellv1.Event_TileRemoved{
		TileRemoved: &gridwellv1.TileRemoved{GridId: connGridID, TileId: local}}})
	return &gridwellv1.DeleteTileResponse{}, nil
}

// ── content streams ──────────────────────────────────────────────────────────

func (s *Server) ReadContent(req *gridwellv1.ReadContentRequest, stream grpc.ServerStreamingServer[gridwellv1.ContentChunk]) error {
	ctx := stream.Context()
	fw, local, err := s.route(ctx, req.TileId)
	if err != nil {
		return err
	}
	if fw != nil {
		cs, err := fw.client.ReadContent(ctx, &gridwellv1.ReadContentRequest{TileId: local})
		if err != nil {
			return err
		}
		return relay(cs, stream)
	}
	c, err := s.localConn(ctx, local)
	if err != nil {
		return err
	}
	// The well's content IS its params document; chunk 1 carries the version
	// the bytes belong to (the save basis, never split).
	return stream.Send(&gridwellv1.ContentChunk{
		Data:      []byte(c.Params),
		MediaType: "application/json",
		Version:   c.Version,
	})
}

// ServeContent forwards web-content requests to the remote node; the
// connection-well tiles themselves serve no pages (a well's content is its
// params document, not a web page).
func (s *Server) ServeContent(req *gridwellv1.ServeContentRequest, stream grpc.ServerStreamingServer[gridwellv1.ServeContentChunk]) error {
	ctx := stream.Context()
	fw, local, err := s.route(ctx, req.TileId)
	if err != nil {
		return err
	}
	if fw == nil {
		return status.Error(codes.Unimplemented, "sshhost: connection wells serve no web content")
	}
	cs, err := fw.client.ServeContent(ctx, &gridwellv1.ServeContentRequest{TileId: local, Subpath: req.Subpath})
	if err != nil {
		return err
	}
	return relay(cs, stream)
}

func (s *Server) WriteContent(stream grpc.ClientStreamingServer[gridwellv1.WriteContentRequest, gridwellv1.TileResponse]) error {
	ctx := stream.Context()
	first, err := stream.Recv()
	if err != nil {
		return status.Error(codes.InvalidArgument, "sshhost: write: empty stream")
	}
	if first.TileId == "" {
		return status.Error(codes.InvalidArgument, "sshhost: write: first message must bind tile_id")
	}
	fw, local, err := s.route(ctx, first.TileId)
	if err != nil {
		return err
	}
	if fw != nil {
		cs, err := fw.client.WriteContent(ctx)
		if err != nil {
			return err
		}
		rewritten := *first
		rewritten.TileId = local
		if err := cs.Send(&rewritten); err != nil {
			return err
		}
		for {
			msg, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				resp, err := cs.CloseAndRecv()
				if err != nil {
					return err
				}
				return stream.SendAndClose(prependTileResp(fw.ns, resp))
			}
			if err != nil {
				return err
			}
			if err := cs.Send(msg); err != nil {
				return err
			}
		}
	}
	// Local: the connection's params document. Accumulate, validate
	// AUTHORITATIVELY, commit at close (a broken stream commits nothing).
	if s.configMode {
		return status.Error(codes.FailedPrecondition, errConfigMode.Error())
	}
	c, err := s.localConn(ctx, local)
	if err != nil {
		return err
	}
	data := append([]byte(nil), first.Data...)
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		data = append(data, msg.Data...)
		if len(data) > 1<<16 {
			return status.Error(codes.InvalidArgument, "sshhost: write: params too large")
		}
	}
	if _, err := ParseParams(data); err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	// The #251 dedup refusal — identical details ARE an existing connection
	// (one param-set, one minted segment); the caller should select it, not
	// mint a twin. The plugin is the authority; the picker's pre-match is
	// only UX.
	want, err := CanonicalParams(data)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	live, err := s.db.List(ctx)
	if err != nil {
		return dbErr(err)
	}
	for _, other := range live {
		if other.ID == c.ID || other.Params == "" {
			continue
		}
		if got, cerr := CanonicalParams([]byte(other.Params)); cerr == nil && got == want {
			name := other.AltText
			if name == "" {
				name = autoLabel(other.Params)
			}
			return status.Errorf(codes.AlreadyExists,
				"sshhost: these details already exist as %q — select that connection instead", name)
		}
	}
	row, err := s.db.SetParams(ctx, c.ID, first.Version, string(data))
	if err != nil {
		return dbErr(err)
	}
	// New endpoint: the old transport (and its cached root) no longer apply.
	s.dropLive(c.NS)
	_ = s.db.BumpGridVersion(ctx)
	s.kickRootFetch(row)
	tile := tileFromConn(row)
	s.hub.publish(&gridwellv1.Event{Payload: &gridwellv1.Event_TileChanged{
		TileChanged: &gridwellv1.TileChanged{Tile: tile}}})
	return stream.SendAndClose(&gridwellv1.TileResponse{Tile: tile})
}

// ── live bytes ───────────────────────────────────────────────────────────────

func (s *Server) OpenShell(stream grpc.BidiStreamingServer[gridwellv1.OpenShellRequest, gridwellv1.OpenShellResponse]) error {
	ctx := stream.Context()
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	fw, local, err := s.route(ctx, first.TileId)
	if err != nil {
		return err
	}
	if fw == nil {
		return status.Error(codes.InvalidArgument, "sshhost: a connection well has no shell")
	}
	cs, err := fw.client.OpenShell(ctx)
	if err != nil {
		return err
	}
	rewritten := *first
	rewritten.TileId = local
	if err := cs.Send(&rewritten); err != nil {
		return err
	}
	errc := make(chan error, 2)
	go func() {
		for {
			msg, err := stream.Recv()
			if err != nil {
				_ = cs.CloseSend()
				errc <- err
				return
			}
			if err := cs.Send(msg); err != nil {
				errc <- err
				return
			}
		}
	}()
	go func() {
		for {
			msg, err := cs.Recv()
			if err != nil {
				errc <- err
				return
			}
			if err := stream.Send(msg); err != nil {
				errc <- err
				return
			}
		}
	}()
	if err := <-errc; err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// ── events ───────────────────────────────────────────────────────────────────

func (s *Server) Subscribe(_ *gridwellv1.SubscribeRequest, stream grpc.ServerStreamingServer[gridwellv1.Event]) error {
	ch, cancel := s.hub.subscribe()
	defer cancel()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(ev); err != nil {
				return err
			}
		case <-stream.Context().Done():
			return nil
		}
	}
}

// ── helpers ──────────────────────────────────────────────────────────────────

func nsOf(fw *forward) string {
	if fw == nil {
		return ""
	}
	return fw.ns
}

// Search forwards the one find verb through the mount (issue #244). An
// `id:` query routes to the connection owning the id, like every routed
// call; free text fans out — to LIVE connections only (a search answers
// with what is reachable; it never dials the world). Each remote answer
// comes from a node SERVER, which fans to its own plugins and mounts —
// the federation recursion falls out of the chain shape. Result ids
// (tiles and paths) get the connection's namespace prepended like every
// other read; a connection that errors or times out contributes nothing.
func (s *Server) Search(ctx context.Context, req *gridwellv1.SearchRequest) (*gridwellv1.SearchResponse, error) {
	if q := rpc.ParseSearchQuery(req.Query); q.ID != "" {
		// An id: query targets ONE connection — an empty answer where the
		// hop actually failed is a lie (it hid the export's missing Search
		// delegate for weeks: every layer read the failure as "not found").
		// Propagate; the fan-out caller upstream decides what a dead hop
		// means for a broader search.
		fw, local, err := s.route(ctx, q.ID)
		if err != nil {
			return nil, err
		}
		if fw == nil {
			return &gridwellv1.SearchResponse{}, nil
		}
		resp, err := fw.client.Search(ctx, &gridwellv1.SearchRequest{Query: "id:" + local, Limit: req.Limit})
		if err != nil {
			return nil, err
		}
		return prependSearchResp(fw.ns, resp), nil
	}
	s.mu.Lock()
	type hop struct {
		ns     string
		client gridwellv1.GridwellClient
	}
	hops := make([]hop, 0, len(s.live))
	for ns, lc := range s.live {
		hops = append(hops, hop{ns, lc.client})
	}
	s.mu.Unlock()
	sort.Slice(hops, func(i, j int) bool { return hops[i].ns < hops[j].ns })
	out := &gridwellv1.SearchResponse{}
	for _, hp := range hops {
		// Each hop bounded (rpc.SearchHopTimeout, shared with the
		// server's fan-out): one hung tunnel must not stall the search.
		hctx, cancel := context.WithTimeout(ctx, rpc.SearchHopTimeout)
		resp, err := hp.client.Search(hctx, &gridwellv1.SearchRequest{Query: req.Query, Limit: req.Limit})
		cancel()
		if err != nil {
			// A hop contributing nothing is policy; contributing nothing
			// SILENTLY is how the missing export delegate stayed invisible.
			log.Printf("gridwell: search: connection %s skipped: %v", hp.ns, err)
			continue
		}
		out.Results = append(out.Results, prependSearchResp(hp.ns, resp).Results...)
	}
	return out, nil
}

// prependSearchResp applies the transit prepend to every id a search
// answer carries — result tiles and their path chains alike.
func prependSearchResp(ns string, resp *gridwellv1.SearchResponse) *gridwellv1.SearchResponse {
	out := &gridwellv1.SearchResponse{Results: make([]*gridwellv1.SearchResult, 0, len(resp.Results))}
	for _, r := range resp.Results {
		qr := &gridwellv1.SearchResult{Snippet: r.Snippet, Score: r.Score}
		if r.Tile != nil {
			qr.Tile = rpc.TransitQualifyTiles(ns, []*gridwellv1.Tile{r.Tile})[0]
		}
		qr.Path = rpc.TransitQualifyTiles(ns, r.Path)
		out.Results = append(out.Results, qr)
	}
	return out
}

func prependTileResp(ns string, resp *gridwellv1.TileResponse) *gridwellv1.TileResponse {
	t := resp.GetTile()
	if t == nil {
		return resp
	}
	return &gridwellv1.TileResponse{Tile: rpc.TransitQualifyTiles(ns, []*gridwellv1.Tile{t})[0]}
}

// stripPrefix removes "<ns>/" from an id qualified from this plugin's frame,
// leaving other ids untouched.
func stripPrefix(id, ns string) string {
	if strings.HasPrefix(id, ns+"/") {
		return id[len(ns)+1:]
	}
	return id
}

type recvStream[T any] interface{ Recv() (*T, error) }
type sendStream[T any] interface{ Send(*T) error }

func relay[T any](from recvStream[T], to sendStream[T]) error {
	for {
		msg, err := from.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := to.Send(msg); err != nil {
			return err
		}
	}
}

// eventHub is the plugin's one local event fan-out: local mutations and the
// per-connection remote fan-ins publish; every Subscribe stream reads.
type eventHub struct {
	mu   sync.Mutex
	next int
	subs map[int]chan *gridwellv1.Event
}

func newEventHub() *eventHub {
	return &eventHub{subs: map[int]chan *gridwellv1.Event{}}
}

func (h *eventHub) subscribe() (<-chan *gridwellv1.Event, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	id := h.next
	h.next++
	ch := make(chan *gridwellv1.Event, 64)
	h.subs[id] = ch
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if c, ok := h.subs[id]; ok {
			delete(h.subs, id)
			close(c)
		}
	}
}

func (h *eventHub) publish(ev *gridwellv1.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subs {
		select {
		case ch <- ev:
		default: // a stalled subscriber drops events rather than blocking mutations
		}
	}
}
