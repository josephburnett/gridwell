package server_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/claimtest"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/sourcecache"
)

// askedIDs is the connection as the cache in front of it sees it, with every
// id it was asked to read, and those it was asked a tile row for.
type askedIDs struct {
	namespace.Namespace
	mu    sync.Mutex
	ids   []string
	tiles []string
}

func (a *askedIDs) note(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ids = append(a.ids, id)
}

func (a *askedIDs) asked() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.ids...)
}

func (a *askedIDs) GetGrid(ctx context.Context, in *gridwellv1.GetGridRequest) (*gridwellv1.GetGridResponse, error) {
	a.note(in.GridId)
	return a.Namespace.GetGrid(ctx, in)
}

func (a *askedIDs) GetTile(ctx context.Context, in *gridwellv1.GetTileRequest) (*gridwellv1.TileResponse, error) {
	a.note(in.TileId)
	a.mu.Lock()
	a.tiles = append(a.tiles, in.TileId)
	a.mu.Unlock()
	return a.Namespace.GetTile(ctx, in)
}

func (a *askedIDs) ReadContent(ctx context.Context, in *gridwellv1.ReadContentRequest, send func(*gridwellv1.ContentChunk) error) error {
	a.note(in.TileId)
	return a.Namespace.ReadContent(ctx, in, send)
}

// The far cache walks the far node's home and never its files: a door onto a
// far directory, and any link, ends the walk there, so a files root as wide
// as a home directory costs the far disk nothing on a reconnect. A directory
// the user opened while connected still reads while dark, from what the
// cache last answered for it (CLAUDE.md, 2026-11-08).
func TestTheFarWalkCoversHomeOnlyAndAVisitedDirectoryReadsDark(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()
	for _, d := range []string{"a/b", "c"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "note.md"), []byte("on disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	var asked *askedIDs
	h := newFrontedTransportHarness(t, []config.ConnectionConfig{{Name: "geneva", Addr: "/s"}}, nil,
		func(ns namespace.Namespace) namespace.Namespace {
			cache, err := sourcecache.Open(t.TempDir() + "/cache.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cache.Close() })
			asked = &askedIDs{Namespace: ns}
			return cache.Front(asked, sourcecache.Options{Prefetch: true})
		},
		filesAt(t, "rnode1", dir))
	farRoot := rpc.QualifyID("rnode1", h.rootBare)
	farDir := rpc.QualifyID("rnode1", rpc.KeyTileID(dir))
	farNote := rpc.QualifyID("rnode1", rpc.KeyTileID(filepath.Join(dir, "note.md")))

	// The far home: an owned door the walk descends, a door onto the files
	// root and a leaf link to a file, neither of which it may follow.
	door, err := h.remoteCl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: farRoot,
		Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.remoteCl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: farRoot,
		Tile: &gridwellv1.Tile{Kind: rpc.KindDoor, X: 2, Y: 0, W: 1, H: 1, ChildGridId: farDir, AltText: "files"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.remoteCl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: farRoot,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 4, Y: 0, W: 1, H: 1, LinkTargetId: farNote, AltText: "note"}}); err != nil {
		t.Fatal(err)
	}

	mark := traceSeq()
	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	go func() {
		_ = h.tClient.Subscribe(subCtx, &gridwellv1.SubscribeRequest{}, func(*gridwellv1.Event) error { return nil })
	}()
	for done := false; !done; {
		for _, rec := range traceSince(mark) {
			done = done || rec.Kind == "prefetch" && rec.KV["state"] == "done"
		}
		select {
		case <-ctx.Done():
			t.Fatal("the walk never finished")
		case <-time.After(10 * time.Millisecond):
		}
	}
	walkedNested := false
	for _, id := range asked.asked() {
		if rpc.IsFilesID(id) {
			t.Errorf("the walk asked the far node's files for %s", id)
		}
		walkedNested = walkedNested || id == "geneva/"+door.ChildGridId
	}
	if !walkedNested {
		t.Errorf("the walk never read the far home's nested grid; asked %v", asked.asked())
	}

	// Opened over the connection, a directory reads live, and is kept.
	opened := "geneva/" + farDir
	live, err := h.tClient.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: opened})
	if err != nil {
		t.Fatalf("opening the far directory: %v", err)
	}
	h.stopFarNode()
	dark, err := h.tClient.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: opened})
	if err != nil {
		t.Fatalf("a far directory visited while connected must read while dark: %v", err)
	}
	if len(dark.Tiles) != len(live.Tiles) || len(live.Tiles) != 3 {
		t.Errorf("the dark directory = %d tiles, live = %d, want 3 each", len(dark.Tiles), len(live.Tiles))
	}
	never := "geneva/" + rpc.QualifyID("rnode1", rpc.KeyTileID(filepath.Join(dir, "a")))
	if _, err := h.tClient.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: never}); err == nil {
		t.Error("a far directory nobody opened read while dark: the walk warmed it")
	}
}

// walkOnce subscribes until a walk starts, waits for it to finish and ends
// the subscription.
func walkOnce(ctx context.Context, t *testing.T, cl namespace.Namespace) {
	t.Helper()
	for {
		mark := traceSeq()
		subCtx, subCancel := context.WithCancel(ctx)
		go func() {
			_ = cl.Subscribe(subCtx, &gridwellv1.SubscribeRequest{}, func(*gridwellv1.Event) error { return nil })
		}()
		started, done := false, false
		for deadline := time.After(time.Second); !done && (started || deadline != nil); {
			for _, rec := range traceSince(mark) {
				started = started || rec.Kind == "prefetch" && rec.KV["state"] == "start"
				done = done || rec.Kind == "prefetch" && rec.KV["state"] == "done"
			}
			select {
			case <-ctx.Done():
				t.Fatal("the walk never finished")
			case <-deadline:
				deadline = nil
			case <-time.After(10 * time.Millisecond):
			}
		}
		subCancel()
		if done {
			return
		}
	}
}

// A leaf link whose target is a gone home row is dead, and the far walk
// reads that verdict: it asks the target once, and again only once the far
// listing holding the link has changed.
func TestTheFarWalkAsksADeadLinkOnceUntilTheListingChanges(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var asked *askedIDs
	h := newFrontedTransportHarness(t, []config.ConnectionConfig{{Name: "geneva", Addr: "/s"}}, nil,
		func(ns namespace.Namespace) namespace.Namespace {
			cache, err := sourcecache.Open(t.TempDir() + "/cache.db")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cache.Close() })
			asked = &askedIDs{Namespace: ns}
			return cache.Front(asked, sourcecache.Options{Prefetch: true})
		})
	farRoot := rpc.QualifyID("rnode1", h.rootBare)
	target, err := h.remoteCl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: farRoot,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.remoteCl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: farRoot,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 2, Y: 0, W: 1, H: 1, LinkTargetId: target.Id}}); err != nil {
		t.Fatal(err)
	}
	for range 2 { // to the trash, then destroyed
		if _, err := h.remoteCl.DeleteTile(ctx, claimtest.Claimed(t, h.remoteCl, &gridwellv1.DeleteTileRequest{TileId: target.Id})); err != nil {
			t.Fatal(err)
		}
	}
	gone := "geneva/" + target.Id
	asks := func() int {
		asked.mu.Lock()
		defer asked.mu.Unlock()
		n := 0
		for _, id := range asked.tiles {
			if id == gone {
				n++
			}
		}
		return n
	}
	for range 3 {
		walkOnce(ctx, t, h.tClient)
	}
	if n := asks(); n != 1 {
		t.Errorf("three walks asked the dead target %d times, want once", n)
	}
	if _, err := h.remoteCl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: farRoot,
		Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 4, Y: 0, W: 1, H: 1}}); err != nil {
		t.Fatal(err)
	}
	walkOnce(ctx, t, h.tClient)
	if n := asks(); n != 2 {
		t.Errorf("a walk after the listing changed asked the dead target %d times in all, want twice", n)
	}
}
