package server_test

// A mesh of real nodes in one process: each node is its own store, router,
// transport, connection door (a real h2c gRPC export) and browser door, and
// each declared connection dials another node's export. Ids cross every hop
// exactly as they do between machines, so a test can ask what happens when a
// verb names two ids behind two different connections, or a link is written
// on one node about a tile on another.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/connection"
	"github.com/josephburnett/gridwell/internal/connection/dial"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

// meshEdge is one declared connection: node From names node To as Name.
type meshEdge struct{ From, Name, To string }

type meshNode struct {
	id   string
	st   *store.Store
	reg  *plugin.Registry
	cl   *rpc.Client // the node's browser door
	root string      // the home's bare root grid id
	door *swapHandler
	far  gridwellv1.GridwellClient
}

type mesh struct {
	t     *testing.T
	nodes map[string]*meshNode
	// asked counts, per "<from>/<name>", what crossed that connection.
	mu    sync.Mutex
	asked map[string]*askCounter
	lands map[string]string // "<from>/<name>" to the node it dials
}

// swapHandler is a connection door whose router a test can replace, the
// in-process stand-in for a node restarting behind the same socket.
type swapHandler struct {
	mu sync.Mutex
	h  http.Handler
}

func (s *swapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	h := s.h
	s.mu.Unlock()
	h.ServeHTTP(w, r)
}

// askCounter is the dialed far namespace with a count of the verbs that decide
// where a clone ran.
type askCounter struct {
	namespace.Namespace
	clones, creates, writes atomic.Int32
}

func (a *askCounter) CloneTile(ctx context.Context, req *gridwellv1.CloneTileRequest) (*gridwellv1.TileResponse, error) {
	a.clones.Add(1)
	return a.Namespace.CloneTile(ctx, req)
}

func (a *askCounter) CreateTile(ctx context.Context, req *gridwellv1.CreateTileRequest) (*gridwellv1.TileResponse, error) {
	a.creates.Add(1)
	return a.Namespace.CreateTile(ctx, req)
}

func (a *askCounter) WriteContent(ctx context.Context, recv func() (*gridwellv1.WriteContentRequest, error)) (*gridwellv1.TileResponse, error) {
	a.writes.Add(1)
	return a.Namespace.WriteContent(ctx, recv)
}

// newMesh stands up every node, then every node's transport over edges, then
// connects them all, so a connection may point anywhere, back edges included.
func newMesh(t *testing.T, ids []string, edges []meshEdge) *mesh {
	t.Helper()
	ctx := context.Background()
	m := &mesh{t: t, nodes: map[string]*meshNode{}, asked: map[string]*askCounter{}, lands: map[string]string{}}
	for _, id := range ids {
		st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		root, err := st.RootGridID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		n := &meshNode{id: id, st: st, root: root, door: &swapHandler{h: http.NotFoundHandler()}}
		hs := httptest.NewUnstartedServer(nil)
		hs.Config = server.ConnectionDoorServer(n.door)
		hs.Start()
		t.Cleanup(hs.Close)
		conn, err := dial.ClientConn(strings.TrimPrefix(hs.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		n.far = gridwellv1.NewGridwellClient(conn)
		n.reg = plugin.NewRegistry()
		n.reg.Register(id, "home", local.New(st, nil), nil)
		n.reg.SetLabel(id, "home of "+id)
		m.nodes[id] = n
	}
	for _, id := range ids {
		m.declare(id, edges, nil)
		n := m.nodes[id]
		srv := servertest.New(t, n.reg, server.Config{ID: id})
		n.door.mu.Lock()
		n.door.h = srv.ConnectionHandler()
		n.door.mu.Unlock()
		web := servertest.Serve(t, srv)
		n.cl = rpc.NewClient(web.Client(), web.URL, connect.WithProtoJSON())
	}
	for _, id := range ids {
		m.connect(id)
	}
	return m
}

// declare gives node id a transport over the edges leaving it, the way serve
// builds one from server.yaml; declaring again is a restart with a new config
// over the same store.
func (m *mesh) declare(id string, edges []meshEdge, retired []string) {
	m.t.Helper()
	n := m.nodes[id]
	var conns []config.ConnectionConfig
	for _, e := range edges {
		if e.From == id {
			m.lands[id+"/"+e.Name] = e.To
			conns = append(conns, config.ConnectionConfig{Name: e.Name, Label: e.To, Addr: "/mesh/" + e.To})
		}
	}
	transport, err := connection.New(n.st, func(cfg dial.Config) (namespace.Namespace, func(), error) {
		to := strings.TrimPrefix(cfg.Addr, "/mesh/")
		if _, ok := m.nodes[to]; !ok {
			return nil, nil, errors.New("no node answers at " + cfg.Addr)
		}
		name := ""
		for _, c := range conns {
			if c.Addr == cfg.Addr {
				name = c.Name
			}
		}
		ac := &askCounter{Namespace: namespace.FromClient(m.nodes[to].far)}
		m.mu.Lock()
		m.asked[id+"/"+name] = ac
		m.mu.Unlock()
		return ac, func() {}, nil
	}, "", conns, retired)
	if err != nil {
		m.t.Fatal(err)
	}
	m.t.Cleanup(func() { _ = transport.Close() })
	n.reg.SetTransport(transport, nil)
}

func (m *mesh) connect(id string) {
	t, _ := m.nodes[id].reg.Transport()
	t.(*connection.Server).ConnectAll(context.Background())
}

// redeclare is a restart of node id with a new connections config.
func (m *mesh) redeclare(id string, edges []meshEdge, retired []string) {
	m.t.Helper()
	m.declare(id, edges, retired)
	m.connect(id)
}

// asks is what crossed the connection "<from>/<name>".
func (m *mesh) asks(from, name string) *askCounter {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.asked[from+"/"+name]; ok {
		return a
	}
	m.t.Fatalf("connection %s/%s was never dialed", from, name)
	return nil
}

// home is node id's home root grid, spelled from node at's frame along the
// connection names in via.
func (m *mesh) home(at string, id string, via ...string) string {
	return m.spell(at, m.nodes[id].root, id, via...)
}

// spell names bare, a row of node id's home, from node at's frame along via.
func (m *mesh) spell(at, bare, id string, via ...string) string {
	return m.spellPath(at, via, rpc.QualifyID(id, bare))
}

// spellPath prepends one "<node>/<conn>" per hop of via, starting at node at,
// onto tail, an id in the frame of the node the hops end on.
func (m *mesh) spellPath(at string, via []string, tail string) string {
	if len(via) == 0 {
		return tail
	}
	node := at
	prefix := ""
	for _, c := range via {
		prefix += node + "/" + c + "/"
		node = m.landing(node, c)
	}
	return prefix + tail
}

// landing is the node a declared connection lands on.
func (m *mesh) landing(from, name string) string {
	to, ok := m.lands[from+"/"+name]
	if !ok {
		m.t.Fatalf("%s declares no connection %s", from, name)
	}
	return to
}

// text creates a text tile with body in grid, through node at's door.
func (m *mesh) text(ctx context.Context, at, grid string, x int64, body string) *gridwellv1.Tile {
	m.t.Helper()
	tl, err := m.nodes[at].cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: grid,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: x, Y: 0, W: 1, H: 1}}, []byte(body))
	if err != nil {
		m.t.Fatalf("create text in %s at %s: %v", grid, at, err)
	}
	return tl
}

// tileIn reads grid through node at and returns the tile id names there.
func (m *mesh) tileIn(ctx context.Context, at, grid, id string) *gridwellv1.Tile {
	m.t.Helper()
	g, err := m.nodes[at].cl.GetGrid(ctx, grid)
	if err != nil {
		m.t.Fatalf("read %s at %s: %v", grid, at, err)
	}
	for _, tl := range g.Tiles {
		if tl.Id == id {
			return tl
		}
	}
	m.t.Fatalf("%s is not in %s", id, grid)
	return nil
}

// count is how many tiles grid holds, read through node at.
func (m *mesh) count(ctx context.Context, at, grid string) int {
	m.t.Helper()
	g, err := m.nodes[at].cl.GetGrid(ctx, grid)
	if err != nil {
		m.t.Fatalf("read %s at %s: %v", grid, at, err)
	}
	return len(g.Tiles)
}
