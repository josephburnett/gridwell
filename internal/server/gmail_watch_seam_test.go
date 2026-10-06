package server

// The gmail plugin's live and lookup verbs through the shipped stack: the
// binary spawned against the recorded Gmail (gmail_seam_test.go), started as
// the loader starts it with its Watch followed, behind the real browser door.
// Each verb is answered by the plugin and read by the node, so only here does
// one side's answer meet the other side's reading of it.

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/grpc"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
)

// gmailClient is the spawned plugin as the node reaches it. It counts the
// plugin's ReadContent calls, which is how a test sees whether anything on
// the node reads a text body for a message, and while unlinked it strips
// link_target from every entry: the shape a plugin from before all mail
// listed, so a test can place rows the way an existing node holds them.
type gmailClient struct {
	pluginv1.PluginClient
	n        atomic.Int32
	unlinked atomic.Bool
}

func (c *gmailClient) ReadContent(ctx context.Context, in *pluginv1.ReadContentRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[pluginv1.ContentChunk], error) {
	c.n.Add(1)
	return c.PluginClient.ReadContent(ctx, in, opts...)
}

func (c *gmailClient) List(ctx context.Context, in *pluginv1.ListRequest, opts ...grpc.CallOption) (*pluginv1.ListResponse, error) {
	resp, err := c.PluginClient.List(ctx, in, opts...)
	if c.unlinked.Load() {
		for _, e := range resp.GetEntries() {
			e.LinkTarget = nil
		}
	}
	return resp, err
}

// gmailNode is the plugin on a node with a home, as `gridwell serve` runs it.
type gmailNode struct {
	srv                 *Server
	cl                  *rpc.Client
	g                   *fakeGmail
	cards               *gmailClient
	home                string // the home grid, qualified
	inbox, starred, all string // the three doorways, qualified
}

// newGmailNode starts the node with the recorded Gmail's new mail held back,
// so a test chooses when it arrives.
func newGmailNode(t *testing.T, refresh string) *gmailNode {
	t.Helper()
	return newGmailNodeOf(t, refresh, func(*gmailClient) {})
}

// newGmailNodeOf is newGmailNode with the client set up by setup before the
// node first reaches the plugin.
func newGmailNodeOf(t *testing.T, refresh string, setup func(*gmailClient)) *gmailNode {
	t.Helper()
	n := &gmailNode{g: newFakeGmail(t)}
	n.g.holdHistory(true)
	cfg, _ := gmailConfig(t, n.g, refresh)
	n.cards = &gmailClient{PluginClient: plugintest.Spawn(t, "gmail", cfg)}
	setup(n.cards)

	st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	reg := plugin.NewRegistry()
	_, n.home = registerPrimaryLocaldb(t, reg, st)
	a, stop := pluginhost.Start(n.cards, st.Namespace(gmailNS), nil, "plugin "+gmailNS+" watch")
	reg.Register(gmailNS, "gmail", a, stop)
	n.srv = mustNew(t, reg, Config{})
	hs := serveWeb(t, n.srv)
	n.cl = rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	lp, err := n.cl.Handshake(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range lp.Plugins {
		if p.Uuid == gmailNS && len(p.MenuEntries) == 3 {
			n.inbox, n.starred, n.all = p.MenuEntries[0].GridId, p.MenuEntries[1].GridId, p.MenuEntries[2].GridId
		}
	}
	if n.inbox == "" {
		t.Fatalf("no gmail doorways in the handshake: %v", lp.Plugins)
	}
	return n
}

// message is the listed inbox tile labelled label: a link into all mail.
func (n *gmailNode) message(t *testing.T, label string) *gridwellv1.Tile {
	t.Helper()
	return n.tile(t, n.inbox, label)
}

// tile is the tile labelled label that grid lists.
func (n *gmailNode) tile(t *testing.T, grid, label string) *gridwellv1.Tile {
	t.Helper()
	g, err := n.cl.GetGrid(t.Context(), grid)
	if err != nil {
		t.Fatal(err)
	}
	return tileWithLabel(t, g, label)
}

// The node counts a Watch stream open when its header arrives, and only then
// lists what the stream adds (pluginhost follow). With an hour's refresh
// nothing can be announced, so a header that waited for a first change would
// never come.
func TestGmailWatchSendsItsHeaderOnAccept(t *testing.T) {
	g := newFakeGmail(t)
	cfg, _ := gmailConfig(t, g, "1h")
	cp := plugintest.Spawn(t, "gmail", cfg)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stream, err := cp.Watch(ctx, &pluginv1.WatchRequest{Contexts: []string{"inbox"}})
	if err != nil {
		t.Fatal(err)
	}
	if md, err := stream.Header(); md == nil {
		t.Fatalf("no header on accept: %v", err)
	}
}

// New mail Gmail's history names reaches a client showing the inbox as the
// inbox's change, without the client asking. (The starred grid may repaint
// too: its links read all mail, which the mail also changed.) The delta is the plugin's to read and the
// announcement the node's to fan out, so neither side alone sees the mail
// reach a client.
func TestGmailHistoryDeltaAnnouncesTheInbox(t *testing.T) {
	n := newGmailNode(t, "200ms")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	events := make(chan string, 64)
	go func() {
		es, err := n.cl.Subscribe(ctx)
		if err != nil {
			return
		}
		defer es.Close()
		for {
			ev, ok, err := es.Recv()
			if err != nil || !ok {
				return
			}
			if id := ev.GetGridChanged().GetGridId(); id != "" {
				events <- id
			}
		}
	}()
	if err := n.cl.SetInterest(ctx, []string{n.inbox, n.starred}); err != nil {
		t.Fatal(err)
	}
	// Nobody read either grid, so the open announces both, and the first walk
	// announces them again. Settled is over a second of nothing: past the
	// refresher's tick, whose held history changes nothing.
	opened := map[string]bool{}
	for settled := false; !settled; {
		select {
		case id := <-events:
			opened[id] = true
		case <-time.After(1500 * time.Millisecond):
			settled = true
		case <-ctx.Done():
			t.Fatalf("the Watch never settled; saw %v", opened)
		}
	}
	if !opened[n.inbox] || !opened[n.starred] {
		t.Fatalf("the open announced %v, want both doorways", opened)
	}

	n.g.holdHistory(false)
	for told := ""; told != n.inbox; {
		select {
		case told = <-events:
		case <-ctx.Done():
			t.Fatal("the history delta never reached the inbox")
		}
	}
	n.message(t, "Quarterly numbers")
}

// Probe is how the node arbitrates a listing and how a far node asks whether
// a tile is still there, and the plugin answers for the context the tile's id
// names, through the router the connection door serves: the inbox's message
// is present in the inbox and in all mail, and gone from the starred grid,
// whose whole read did not list it. Leaving a label is not being gone.
func TestGmailProbeAnswersForTheContextAsked(t *testing.T) {
	n := newGmailNode(t, "1h")
	n.tile(t, n.starred, "Invoice 41") // the starred grid has been read
	link := n.message(t, "Lunch plans")
	key := "msg:18c2a1b3f4d5e6f7" // Lunch plans, in the recorded inbox only
	for id, want := range map[string]gridwellv1.ProbeResponse_Presence{
		link.Id:           gridwellv1.ProbeResponse_PRESENCE_PRESENT,
		link.LinkTargetId: gridwellv1.ProbeResponse_PRESENCE_PRESENT,
		rpc.QualifyID(gmailNS, rpc.EntryTileID("label:STARRED", key)): gridwellv1.ProbeResponse_PRESENCE_GONE,
	} {
		resp, err := newRouter(n.srv).Probe(t.Context(), &gridwellv1.ProbeRequest{TileId: id})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Presence != want {
			t.Errorf("Probe(%s) = %v, want %v", id, resp.Presence, want)
		}
	}
}

// A search hit lands on the message's one tile, the one all mail lists — the
// same id, so the hit descends into what the grid shows.
func TestGmailSearchLandsOnTheAllMailTile(t *testing.T) {
	n := newGmailNode(t, "1h")
	want := n.tile(t, n.all, "Invoice 41")
	resp, err := n.cl.Search(t.Context(), "invoice", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.GetResults(); len(got) != 1 || got[0].GetTile().GetId() != want.Id {
		t.Fatalf("search invoice = %v, want all mail's %s", resp, want.Id)
	}
}

// A grid the plugin remembers survives Gmail going away: the plugin answers
// its memory with the reason in unreachable, and the node keeps serving every
// row. The memory is the plugin's and the rows the node's, so only the seam
// shows an outage costing the user nothing on screen.
func TestGmailWarmGridSurvivesGmailGoingAway(t *testing.T) {
	n := newGmailNode(t, "200ms")
	ctx := t.Context()
	before := map[string]int{}
	for _, grid := range []string{n.inbox, n.starred, n.all} {
		g, err := n.cl.GetGrid(ctx, grid)
		if err != nil {
			t.Fatal(err)
		}
		before[grid] = len(g.Tiles)
	}

	n.g.Stop()
	deadline := time.Now().Add(15 * time.Second)
	for {
		resp, err := n.cards.PluginClient.List(ctx, &pluginv1.ListRequest{Context: "label:INBOX"})
		if err != nil {
			t.Fatalf("a warm list with Gmail gone = %v, want memory's answer", err)
		}
		if resp.Unreachable != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the plugin never said Gmail was unreachable")
		}
		time.Sleep(100 * time.Millisecond)
	}
	for _, grid := range []string{n.inbox, n.starred, n.all} {
		g, err := n.cl.GetGrid(ctx, grid)
		if err != nil {
			t.Fatalf("%s with Gmail gone = %v", grid, err)
		}
		if len(g.Tiles) != before[grid] {
			t.Errorf("%s with Gmail gone shows %d tiles, want the %d it showed", grid, len(g.Tiles), before[grid])
		}
	}
}

// A row placed in a label while its entry was a page of its own — every row
// an existing node holds from before all mail — becomes a link in place when
// the plugin lists the link: the same id, where the user put it.
func TestGmailPlacedLabelRowsBecomeLinksInPlace(t *testing.T) {
	n := newGmailNodeOf(t, "1h", func(c *gmailClient) { c.unlinked.Store(true) })
	ctx := t.Context()
	placed := map[string]*gridwellv1.Tile{}
	for i, grid := range []string{n.inbox, n.starred} {
		before := n.tile(t, grid, "Invoice 41")
		if before.Reference {
			t.Fatalf("%s drew a link from an entry with no link_target", grid)
		}
		if _, err := n.cl.PlaceTile(ctx, &gridwellv1.PlaceTileRequest{TileId: before.Id, X: 7, Y: int64(-2 - i), W: 2, H: 1}); err != nil {
			t.Fatal(err)
		}
		placed[grid] = before
	}

	n.cards.unlinked.Store(false)
	target := n.tile(t, n.all, "Invoice 41")
	for i, grid := range []string{n.inbox, n.starred} {
		after := n.tile(t, grid, "Invoice 41")
		if after.Id != placed[grid].Id || after.X != 7 || after.Y != int64(-2-i) || after.W != 2 {
			t.Errorf("%s: after = %s at (%d,%d) w%d, want %s where the user put it", grid, after.Id, after.X, after.Y, after.W, placed[grid].Id)
		}
		if !after.Reference || after.LinkTargetId != target.Id {
			t.Errorf("%s: after = reference %v target %q, want a link to %s", grid, after.Reference, after.LinkTargetId, target.Id)
		}
	}
}

// The trash gesture on a message is refused, and the plugin's reason is what
// the client reads: a refusal that arrived as a bare code would look like a
// delete that failed to stick. The message stays listed.
func TestGmailDeleteIsRefusedWithItsReason(t *testing.T) {
	n := newGmailNode(t, "1h")
	msg := n.message(t, "Lunch plans")
	_, err := n.cl.DeleteTile(t.Context(), &gridwellv1.DeleteTileRequest{TileId: msg.Id})
	if connect.CodeOf(err) != connect.CodeUnimplemented || !strings.Contains(err.Error(), "read-only projection") {
		t.Fatalf("DeleteTile = %v, want the plugin's read-only refusal", err)
	}
	n.message(t, "Lunch plans")
}

// A message has no text body, and nothing the node does for a url entry asks
// for one: listing, serving the email, and a cross-plugin clone all read
// zero. Every reader of a body gates on a body kind (rpc.IsBodyKind: the deep
// copy, the cache's prefetch; rpc.TextDocument: the client's descent), and a
// message is a url, so the plugin serves no ReadContent at all.
func TestGmailURLEntryNeedsNoTextBody(t *testing.T) {
	n := newGmailNode(t, "1h")
	msg := n.tile(t, n.all, "Lunch plans")
	if rpc.IsBodyKind(msg.Kind) || rpc.TextDocument(msg) {
		t.Fatalf("a message is kind %q; this test's premise is a url", msg.Kind)
	}

	if _, err := n.cl.CloneTile(t.Context(), &gridwellv1.CloneTileRequest{
		TileId: msg.Id, DestGridId: n.home, X: 3, Y: 3,
	}); err != nil {
		t.Fatalf("clone the message home: %v", err)
	}
	if got := n.cards.n.Load(); got != 0 {
		t.Fatalf("the node read %d text bodies for a url entry", got)
	}
}
