package server

// A plugin entry that links to another of its entries (Entry.link_target),
// through the whole stack: the adapter, the router, the browser door and the
// store's file. The target is an entry nobody has touched, so every hop must
// resolve it by its address alone.

import (
	"context"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

const linkPluginUUID = "lnk-test-uuid"

// mailSource is a mailbox in miniature: "everything" holds each thread once,
// and "box" lists a link per thread into it. A box key is not the thread's
// key, so a read that did not go through the target would not find the card.
type mailSource struct {
	pluginv1.UnimplementedPluginServer

	mu      sync.Mutex
	gone    bool   // everything no longer holds t1, and says so on Probe
	card    string // t1's card
	asLink  bool   // the box lists t1 as a link; else as a page of its own
	inBox   bool   // the box lists t1 at all
	probes  []string
	watches chan []string
	changes chan *pluginv1.Change
}

func newMailSource() *mailSource {
	return &mailSource{card: "card of t1", asLink: true, inBox: true,
		watches: make(chan []string, 16), changes: make(chan *pluginv1.Change, 16)}
}

func (m *mailSource) set(edit func(*mailSource)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	edit(m)
}

func (m *mailSource) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return &pluginv1.InfoResponse{Kind: "mailish", DisplayName: "mail", Watch: true, MenuEntries: []*pluginv1.MenuEntry{
		{Id: "box", Label: "box", Context: "box"}, {Id: "everything", Label: "everything", Context: "everything"}}}, nil
}

func (m *mailSource) List(_ context.Context, req *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	resp := &pluginv1.ListResponse{}
	switch req.Context {
	case "everything":
		if !m.gone {
			resp.Entries = append(resp.Entries, &pluginv1.Entry{Key: "t1", Kind: rpc.KindText, Label: "Lunch"})
		}
	case "box":
		if m.inBox {
			e := &pluginv1.Entry{Key: "box:t1", Kind: rpc.KindText, Label: "● Lunch", StatusDetail: "unseen"}
			if m.asLink {
				e.LinkTarget = &pluginv1.EntryRef{Context: "everything", Key: "t1"}
			}
			resp.Entries = append(resp.Entries, e)
		}
	default:
		return nil, status.Errorf(codes.InvalidArgument, "no context %q", req.Context)
	}
	return resp, nil
}

func (m *mailSource) Probe(_ context.Context, req *pluginv1.ProbeRequest) (*pluginv1.ProbeResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.probes = append(m.probes, req.Context+"/"+req.Key)
	if req.Context == "everything" && m.gone || req.Context == "box" && !m.inBox {
		return &pluginv1.ProbeResponse{Presence: pluginv1.ProbeResponse_PRESENCE_GONE}, nil
	}
	return &pluginv1.ProbeResponse{Presence: pluginv1.ProbeResponse_PRESENCE_PRESENT}, nil
}

func (m *mailSource) probed() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.probes)
}

func (m *mailSource) ReadContent(req *pluginv1.ReadContentRequest, s pluginv1.Plugin_ReadContentServer) error {
	m.mu.Lock()
	card := m.card
	m.mu.Unlock()
	if req.Key != "t1" {
		return status.Errorf(codes.NotFound, "no thread %q: a box key owns no content", req.Key)
	}
	return s.Send(&pluginv1.ContentChunk{Data: []byte(card), MediaType: "text/markdown"})
}

func (m *mailSource) GetPreview(context.Context, *pluginv1.GetPreviewRequest) (*pluginv1.GetPreviewResponse, error) {
	return &pluginv1.GetPreviewResponse{}, nil
}

func (m *mailSource) Watch(req *pluginv1.WatchRequest, s pluginv1.Plugin_WatchServer) error {
	m.watches <- slices.Clone(req.Contexts)
	if err := s.SendHeader(nil); err != nil {
		return err
	}
	for {
		select {
		case <-s.Context().Done():
			return nil
		case ch := <-m.changes:
			if err := s.Send(ch); err != nil {
				return err
			}
		}
	}
}

func linkStack(t *testing.T, src *mailSource) (*rpc.Client, *store.Store, string) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	reg := plugin.NewRegistry()
	registerPrimaryLocaldb(t, reg, st)
	cp, closer, err := plugintest.Loopback(src)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closer)
	a, stop := pluginhost.Start(cp, st.Namespace(linkPluginUUID), nil, "mail watch")
	t.Cleanup(stop)
	reg.Register(linkPluginUUID, "mailish", a, nil)
	h := serveWeb(t, mustNew(t, reg, Config{}))
	cl := rpc.NewClient(h.Client(), h.URL, connect.WithProtoJSON())
	lp, err := cl.Handshake(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return cl, st, rpc.HomeGrid(lp)
}

func boxGrid() string { return rpc.QualifyID(linkPluginUUID, rpc.EntryGridID("box")) }

func threadID() string { return rpc.QualifyID(linkPluginUUID, rpc.EntryTileID("everything", "t1")) }

func boxLink(t *testing.T, cl *rpc.Client) *gridwellv1.Tile {
	t.Helper()
	g, err := cl.GetGrid(context.Background(), boxGrid())
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Tiles) != 1 {
		t.Fatalf("box = %+v, want one tile", g.Tiles)
	}
	return g.Tiles[0]
}

// A box entry that links to a thread nobody has touched is a link to that
// thread at every hop: drawn as one, read, previewed and descended through
// the thread, copied as another link to it, and dead when the thread is gone.
func TestALinkEntryResolvesThroughItsUntouchedTarget(t *testing.T) {
	src := newMailSource()
	cl, st, home := linkStack(t, src)
	ctx := context.Background()

	link := boxLink(t, cl)
	if !link.Reference || link.LinkTargetId != threadID() {
		t.Fatalf("box tile = reference %v target %q, want a link to %q", link.Reference, link.LinkTargetId, threadID())
	}
	if link.Kind != rpc.KindText || link.AltText != "● Lunch" || link.StatusDetail != "unseen" {
		t.Fatalf("box tile = kind %q label %q status %q, want the box's own label on the target's kind",
			link.Kind, link.AltText, link.StatusDetail)
	}
	body, _, _, err := cl.ReadContent(ctx, link.Id)
	if err != nil || string(body) != "card of t1" {
		t.Fatalf("ReadContent through the link = %q, %v; want the thread's card", body, err)
	}
	if _, err := cl.GetTilePreview(ctx, link.Id); err != nil {
		t.Fatalf("GetTilePreview through the link: %v", err)
	}
	if target, err := cl.GetTile(ctx, link.LinkTargetId); err != nil || target.Id != link.LinkTargetId {
		t.Fatalf("descent to the target = %+v, %v", target, err)
	}
	if tiles, _ := pluginRows(t, st); tiles != 0 {
		t.Fatalf("reading through a link minted %d rows, want none", tiles)
	}

	// A copy of a link is another link to the same target.
	clone, err := cl.CloneTile(ctx, &gridwellv1.CloneTileRequest{TileId: link.Id, DestGridId: home, X: 3, Y: 3})
	if err != nil {
		t.Fatal(err)
	}
	if clone.LinkTargetId != threadID() {
		t.Fatalf("the clone links to %q, want the thread %q", clone.LinkTargetId, threadID())
	}

	src.set(func(m *mailSource) { m.gone = true })
	for _, id := range []string{link.Id, clone.Id} {
		if _, _, _, err := cl.ReadContent(ctx, id); !gwerr.IsDeadRef(err) {
			t.Errorf("ReadContent(%s) once the thread is gone = %v, want the dead verdict", id, err)
		}
		if _, err := cl.GetTilePreview(ctx, id); !gwerr.IsDeadRef(err) {
			t.Errorf("GetTilePreview(%s) once the thread is gone = %v, want the dead verdict", id, err)
		}
	}
	if !slices.Contains(src.probed(), "everything/t1") {
		t.Fatalf("probes = %v, want the target asked about in its own context", src.probed())
	}

	src.set(func(m *mailSource) { m.gone = false })
	if body, _, _, err := cl.ReadContent(ctx, clone.Id); err != nil || string(body) != "card of t1" {
		t.Fatalf("the thread is back and the clone reads %q, %v", body, err)
	}
}

// A box row the user touched while its entry owned content becomes a link
// in place when the entry does: the same id and placement, now a link.
func TestATouchedEntryThatBecomesALinkKeepsItsPlace(t *testing.T) {
	src := newMailSource()
	src.asLink = false
	cl, _, _ := linkStack(t, src)
	ctx := context.Background()

	before := boxLink(t, cl)
	if before.Reference {
		t.Fatal("an entry with no link_target drew as a link")
	}
	if _, err := cl.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{TileId: before.Id, X: 7, Y: -2, W: 2, H: 1}); err != nil {
		t.Fatal(err)
	}
	src.set(func(m *mailSource) { m.asLink = true })
	after := boxLink(t, cl)
	if after.Id != before.Id || after.X != 7 || after.Y != -2 || after.W != 2 {
		t.Fatalf("after = %s at (%d,%d) w%d, want %s where the user put it", after.Id, after.X, after.Y, after.W, before.Id)
	}
	if !after.Reference || after.LinkTargetId != threadID() {
		t.Fatalf("after = reference %v target %q, want a link to the thread", after.Reference, after.LinkTargetId)
	}
}

// A touched box row its box stops listing is asked about in the box: a
// thread that left the box is not a thread that is gone.
func TestTheSweepProbesTheContextItSweeps(t *testing.T) {
	src := newMailSource()
	cl, _, _ := linkStack(t, src)
	ctx := context.Background()
	link := boxLink(t, cl)
	if _, err := cl.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{TileId: link.Id, X: 4, Y: 4, W: 1, H: 1}); err != nil {
		t.Fatal(err)
	}
	src.set(func(m *mailSource) { m.inBox = false })
	g, err := cl.GetGrid(ctx, boxGrid())
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Tiles) != 0 {
		t.Fatalf("box = %+v, want the row its box says it left retired", g.Tiles)
	}
	if !slices.Contains(src.probed(), "box/box:t1") {
		t.Fatalf("probes = %v, want box:t1 asked about in the box", src.probed())
	}
	if _, err := cl.GetTile(ctx, threadID()); err != nil {
		t.Fatalf("the thread itself = %v; leaving a box is not gone", err)
	}
}

// An entry the node cannot draw as a link is refused with the reason.
func TestALinkTheNodeCannotDrawIsRefused(t *testing.T) {
	for name, e := range map[string]*pluginv1.Entry{
		"no key":    {Key: "a", Kind: rpc.KindText, LinkTarget: &pluginv1.EntryRef{Context: "everything"}},
		"a well":    {Key: "a", Kind: rpc.KindWell, ChildContext: "c", LinkTarget: &pluginv1.EntryRef{Context: "everything", Key: "t1"}},
		"to itself": {Key: "a", Kind: rpc.KindText, LinkTarget: &pluginv1.EntryRef{Context: "box", Key: "a"}},
	} {
		t.Run(name, func(t *testing.T) {
			src := &oneEntrySource{entry: e}
			st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			cp, closer, err := plugintest.Loopback(src)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(closer)
			a := pluginhost.New(cp, st.Namespace(linkPluginUUID), nil)
			_, err = a.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: rpc.EntryGridID("box")})
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("GetGrid = %v, want InvalidArgument with the reason", err)
			}
		})
	}
}

type oneEntrySource struct {
	pluginv1.UnimplementedPluginServer
	entry *pluginv1.Entry
}

func (o *oneEntrySource) Info(context.Context, *pluginv1.InfoRequest) (*pluginv1.InfoResponse, error) {
	return &pluginv1.InfoResponse{Kind: "one", MenuEntries: []*pluginv1.MenuEntry{{Id: "box", Context: "box"}}}, nil
}

func (o *oneEntrySource) List(context.Context, *pluginv1.ListRequest) (*pluginv1.ListResponse, error) {
	return &pluginv1.ListResponse{Entries: []*pluginv1.Entry{o.entry}}, nil
}

// A client showing only the box is told when a thread its links point at
// changes, though nobody shows everything: the node watches the contexts a
// shown grid links into, and announces the holder when one changes. Its next
// read through the link is the new card.
func TestATargetsChangeReachesAGridThatOnlyLinksToIt(t *testing.T) {
	src := newMailSource()
	cl, _, _ := linkStack(t, src)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	link := boxLink(t, cl)
	if body, _, _, err := cl.ReadContent(ctx, link.Id); err != nil || string(body) != "card of t1" {
		t.Fatalf("first read = %q, %v", body, err)
	}

	events := make(chan *gridwellv1.Event, 64)
	go func() {
		es, err := cl.Subscribe(ctx)
		if err != nil {
			return
		}
		defer es.Close()
		for {
			ev, ok, err := es.Recv()
			if err != nil || !ok {
				return
			}
			events <- ev
		}
	}()
	if err := cl.SetInterest(ctx, []string{boxGrid()}); err != nil {
		t.Fatal(err)
	}
	for watched := false; !watched; {
		select {
		case scope := <-src.watches:
			watched = slices.Contains(scope, "everything")
		case <-ctx.Done():
			t.Fatal("the Watch scope never named the context the box links into")
		}
	}

	src.set(func(m *mailSource) { m.card = "card of t1, read" })
	src.changes <- &pluginv1.Change{Payload: &pluginv1.Change_ContextChanged{
		ContextChanged: &pluginv1.ContextChanged{Context: "everything"}}}
	for told := false; !told; {
		select {
		case ev := <-events:
			told = ev.GetGridChanged().GetGridId() == boxGrid()
		case <-ctx.Done():
			t.Fatal("a change to the thread never reached the box that links to it")
		}
	}
	if body, _, _, err := cl.ReadContent(ctx, link.Id); err != nil || string(body) != "card of t1, read" {
		t.Fatalf("the read after the announcement = %q, %v; want the new card", body, err)
	}
}
