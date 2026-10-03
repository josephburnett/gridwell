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

// cardReads counts the plugin's ReadContent calls, which is how a test sees
// whether anything on the node reads a message's markdown card.
type cardReads struct {
	pluginv1.PluginClient
	n atomic.Int32
}

func (c *cardReads) ReadContent(ctx context.Context, in *pluginv1.ReadContentRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[pluginv1.ContentChunk], error) {
	c.n.Add(1)
	return c.PluginClient.ReadContent(ctx, in, opts...)
}

// gmailNode is the plugin on a node with a home, as `gridwell serve` runs it.
type gmailNode struct {
	srv            *Server
	cl             *rpc.Client
	g              *fakeGmail
	cards          *cardReads
	home           string // the home grid, qualified
	inbox, starred string // the two doorways, qualified
}

// newGmailNode starts the node with the recorded Gmail's new mail held back,
// so a test chooses when it arrives.
func newGmailNode(t *testing.T, refresh string) *gmailNode {
	t.Helper()
	n := &gmailNode{g: newFakeGmail(t)}
	n.g.holdHistory(true)
	cfg, _ := gmailConfig(t, n.g, refresh)
	n.cards = &cardReads{PluginClient: plugintest.Spawn(t, "gmail", cfg)}

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
		if p.Uuid == gmailNS && len(p.MenuEntries) == 2 {
			n.inbox, n.starred = p.MenuEntries[0].GridId, p.MenuEntries[1].GridId
		}
	}
	if n.inbox == "" {
		t.Fatalf("no gmail doorways in the handshake: %v", lp.Plugins)
	}
	return n
}

// message is the listed inbox tile labelled label.
func (n *gmailNode) message(t *testing.T, label string) *gridwellv1.Tile {
	t.Helper()
	g, err := n.cl.GetGrid(t.Context(), n.inbox)
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
// inbox's change, without the client asking, and the starred grid it did not
// touch is not announced. The delta is the plugin's to read and the
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
	select {
	case id := <-events:
		if id != n.inbox {
			t.Fatalf("the delta announced %s, want the inbox %s", id, n.inbox)
		}
	case <-ctx.Done():
		t.Fatal("the history delta never reached the client")
	}
	select {
	case id := <-events:
		t.Fatalf("after the inbox the client was told %s; the delta touched nothing else", id)
	case <-time.After(time.Second):
	}
	n.message(t, "Quarterly numbers")
}

// Probe is how the node arbitrates a non-authoritative listing and how a far
// node asks whether a tile is still there; for a message the inbox lists the
// answer is present, through the router the connection door serves.
func TestGmailProbeAnswersPresentForAListedMessage(t *testing.T) {
	n := newGmailNode(t, "1h")
	msg := n.message(t, "Lunch plans")
	resp, err := newRouter(n.srv).Probe(t.Context(), &gridwellv1.ProbeRequest{TileId: msg.Id})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Presence != gridwellv1.ProbeResponse_PRESENCE_PRESENT {
		t.Errorf("Probe(%s) = %v, want present", msg.Id, resp.Presence)
	}
}

// A search placed by the plugin's context path lands on the tile the inbox
// lists — the same id, so the hit descends into what the grid shows.
func TestGmailSearchAnswersWithTheListedTile(t *testing.T) {
	n := newGmailNode(t, "1h")
	want := n.message(t, "Invoice 41")
	got, err := n.cl.Search(t.Context(), "invoice", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].GetTile().GetId() != want.Id {
		t.Fatalf("search invoice = %v, want the inbox's %s", got, want.Id)
	}
}

// The trash gesture on a message is refused, and the plugin's reason is what
// the client reads: a refusal that arrived as a bare code would look like a
// delete that failed to stick. The message stays listed.
func TestGmailDeleteIsRefusedWithItsReason(t *testing.T) {
	n := newGmailNode(t, "1h")
	msg := n.message(t, "Lunch plans")
	err := n.cl.DeleteTile(t.Context(), &gridwellv1.DeleteTileRequest{TileId: msg.Id})
	if connect.CodeOf(err) != connect.CodeUnimplemented || !strings.Contains(err.Error(), "read-only projection") {
		t.Fatalf("DeleteTile = %v, want the plugin's read-only refusal", err)
	}
	n.message(t, "Lunch plans")
}

// A message's markdown card answers ReadContent when asked for, but nothing
// the node does for a url entry asks: listing, serving the email, and a
// cross-plugin clone all read zero cards. Every reader of a body gates on a
// body kind (rpc.IsBodyKind: the deep copy, the cache's prefetch;
// rpc.TextDocument: the client's descent), and a message is a url.
func TestGmailCardIsReadByNothingForAURLEntry(t *testing.T) {
	n := newGmailNode(t, "1h")
	msg := n.message(t, "Lunch plans")
	if rpc.IsBodyKind(msg.Kind) || rpc.TextDocument(msg) {
		t.Fatalf("a message is kind %q; this test's premise is a url", msg.Kind)
	}

	if _, err := n.cl.CloneTile(t.Context(), &gridwellv1.CloneTileRequest{
		TileId: msg.Id, DestGridId: n.home, X: 3, Y: 3,
	}); err != nil {
		t.Fatalf("clone the message home: %v", err)
	}
	if got := n.cards.n.Load(); got != 0 {
		t.Fatalf("the node read %d cards for a url entry", got)
	}

	data, media, _, err := n.cl.ReadContent(t.Context(), msg.Id)
	if err != nil {
		t.Fatal(err)
	}
	if media != "text/markdown" || !strings.Contains(string(data), "Lunch plans") {
		t.Errorf("the card = %q %q", media, data)
	}
}
