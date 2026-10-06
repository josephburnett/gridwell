package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"github.com/josephburnett/gridwell/internal/plugintest/heyfake"
)

// heyShown is a client showing hey's everything over the real binary, with
// one thread on it.
type heyShown struct {
	ctx    context.Context
	cl     *rpc.Client
	events chan *gridwellv1.Event
	feed   *heyfake.Feed
	shown  string
	th     heyfake.Thread
	tile   *gridwellv1.Tile
}

func showHeyThread(t *testing.T) *heyShown {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	hey := heyAccount(t)
	cp := plugintest.Spawn(t, "hey", hey.Config(nil))
	a, stop := pluginhost.Start(cp, st.Namespace(heyNS), nil, "plugin "+heyNS+" watch")
	reg := plugin.NewRegistry()
	reg.Register(heyNS, "hey", a, stop)
	hs := serveWeb(t, mustNew(t, reg, Config{}))
	s := &heyShown{cl: rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON()),
		events: make(chan *gridwellv1.Event, 256)}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	s.ctx = ctx
	lp, err := s.cl.Handshake(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range lp.Plugins {
		for _, m := range p.MenuEntries {
			if p.Uuid == heyNS && m.Label == "everything" {
				s.shown = m.GridId
			}
		}
	}
	if s.shown == "" {
		t.Fatal("no everything in the handshake")
	}
	go func() {
		es, err := s.cl.Subscribe(ctx)
		if err != nil {
			return
		}
		defer es.Close()
		for {
			ev, ok, err := es.Recv()
			if err != nil || !ok {
				return
			}
			s.events <- ev
		}
	}()
	if err := s.cl.SetInterest(ctx, []string{s.shown}); err != nil {
		t.Fatal(err)
	}
	s.feed = hey.AwaitFeed(t)
	s.feed.Ready(t)
	at := time.Date(2026, 1, 9, 19, 1, 4, 0, time.UTC)
	s.th = heyfake.Thread{TopicID: 103, Subject: "Board games", Summary: "Thursday at mine?", From: "Erin",
		Email: "erin@example.com", Created: at, Active: at}
	s.feed.Add(t, "imbox", s.th)
	for s.tile == nil {
		select {
		case ev := <-s.events:
			if ev.GetGridChanged().GetGridId() != s.shown {
				continue
			}
			s.tile = s.thread(t)
		case <-ctx.Done():
			t.Fatal("the thread never reached the client")
		}
	}
	return s
}

// thread is the thread's row as the grid serves it now, nil before it lands.
func (s *heyShown) thread(t *testing.T) *gridwellv1.Tile {
	t.Helper()
	g, err := s.cl.GetGrid(s.ctx, s.shown)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range g.Tiles {
		if tl.AltText == "Erin: Board games" {
			return tl
		}
	}
	return nil
}

// reply lands a reply on the thread and answers the event that tells it:
// the thread's TileChanged under the stamp HEY moved (its active_at).
func (s *heyShown) reply(t *testing.T) *gridwellv1.Event {
	t.Helper()
	s.th.Active = s.th.Active.Add(time.Hour)
	s.feed.Add(t, "imbox", s.th)
	want := s.th.Active.Format(time.RFC3339Nano)
	for {
		select {
		case ev := <-s.events:
			tc := ev.GetTileChanged()
			if tc.GetTile().GetId() == s.tile.Id && tc.GetContentChanged() && tc.GetTile().GetContentStamp() == want {
				return ev
			}
		case <-s.ctx.Done():
			t.Fatalf("the reply never reached the client as thread %s changed in place under %s", s.tile.Id, want)
		}
	}
}

// capture writes the screenshot a closing live view leaves on the thread and
// answers its face key.
func (s *heyShown) capture(t *testing.T, jpeg string) int64 {
	t.Helper()
	got, err := s.cl.SetTile(s.ctx, &gridwellv1.SetTileRequest{TileId: s.tile.Id,
		Tile: &gridwellv1.Tile{Kind: rpc.KindURL}, Preview: []byte(jpeg)})
	if err != nil {
		t.Fatal(err)
	}
	if got.PreviewBlobId <= 0 {
		t.Fatalf("the capture left face key %d, want the screenshot's", got.PreviewBlobId)
	}
	return got.PreviewBlobId
}

// A reply landing on a thread a client shows reaches it as that thread's
// tile changed in place, on the real binary: the shipped plugin tells it as
// its entry in everything (plugin standard rule 18).
func TestHeyReplyReachesTheClientAsItsThreadsChange(t *testing.T) {
	s := showHeyThread(t)
	s.reply(t)
}

// A reply moves the page a thread's screenshot pictures, so the screenshot
// is no longer its face: the event carries the face key without it, and the
// grid serves the same until the next capture.
func TestAHeyReplyRetiresTheThreadsScreenshotFace(t *testing.T) {
	s := showHeyThread(t)
	shot := s.capture(t, "\xff\xd8\xff before the reply")
	ev := s.reply(t)
	if got := ev.GetTileChanged().GetTile().GetPreviewBlobId(); got > 0 {
		t.Fatalf("the reply's event keeps face key %d, a screenshot of the page before it (%d)", got, shot)
	}
	if got := s.thread(t).GetPreviewBlobId(); got > 0 {
		t.Fatalf("the grid serves face key %d after the reply, a screenshot of the page before it (%d)", got, shot)
	}
	next := s.capture(t, "\xff\xd8\xff after the reply")
	if got := s.thread(t).GetPreviewBlobId(); got != next {
		t.Fatalf("the grid serves face key %d after the next capture, want %d", got, next)
	}
}

// A screenshot the user took with the freeze gesture stays the face through
// a reply: the standing freeze is the exception.
func TestAFrozenHeyThreadKeepsItsScreenshotThroughAReply(t *testing.T) {
	s := showHeyThread(t)
	shot := s.capture(t, "\xff\xd8\xff frozen")
	if _, err := s.cl.SetFrozen(s.ctx, s.tile.Id, true); err != nil {
		t.Fatal(err)
	}
	ev := s.reply(t)
	if got := ev.GetTileChanged().GetTile().GetPreviewBlobId(); got != shot {
		t.Fatalf("the reply's event moved a frozen thread's face key %d → %d", shot, got)
	}
	if got := s.thread(t).GetPreviewBlobId(); got != shot {
		t.Fatalf("the grid serves face key %d for a frozen thread after the reply, want its screenshot %d", got, shot)
	}
}
