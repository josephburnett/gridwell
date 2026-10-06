package server_test

// A text tile behaves the same wherever its content comes from: an fs file
// is typed into and saved as a home document is, through the client's own
// owners (cache, textedit.SaveClaim, clientsync, outbox) over the real node
// and the real fs binary. postWriteContent is the shim's spelling of a save;
// save below is that spelling.

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/clientsync"
	"github.com/josephburnett/gridwell/client/outbox"
	"github.com/josephburnett/gridwell/client/textedit"
	"github.com/josephburnett/gridwell/internal/namespace"
)

// save is postWriteContent: claim, write, then on success advance from the
// response row; on a verdict drop the refused bytes; either way the entry's
// dirtiness is whether the write is still owed.
func (k *contentClient) save(out *outbox.Outbox, id string) error {
	data, dirty := k.c.DirtyContent(id)
	if !dirty {
		out.RecordContent(id, false, nil)
		return nil
	}
	basis, have := k.c.SaveBasis(id)
	claim := textedit.SaveClaim(true, rpc.BasisOf(k.row(id)), basis, have)
	tile, err := k.cl.WriteContent(k.ctx, id, claim, data)
	if err != nil {
		if clientsync.ReactSave(clientsync.Of(err)).DropLocal {
			k.c.DropTileContent(id)
		}
		_, still := k.c.DirtyContent(id)
		out.RecordContent(id, still, func() { _ = k.save(out, id) })
		return err
	}
	k.c.PutWriteResponse(tile.GridId, tile, cache.WroteBody)
	k.c.PutSavedContent(tile, data)
	_, still := k.c.DirtyContent(id)
	out.RecordContent(id, still, func() { _ = k.save(out, id) })
	return nil
}

// row is the landing's cached row for id.
func (k *contentClient) row(id string) *gridwellv1.Tile {
	k.t.Helper()
	g, ok := k.c.Grid(k.landing)
	if !ok || g.Tiles[id] == nil {
		k.t.Fatalf("no row %q in the landing", id)
	}
	return g.Tiles[id]
}

// stampNow is the stamp a fresh read names.
func (k *contentClient) stampNow(id string) string {
	k.t.Helper()
	_, _, b, err := k.cl.ReadContent(k.ctx, id)
	if err != nil {
		k.t.Fatal(err)
	}
	return b.Stamp
}

// A plugin that declares writable serves grids whose bodies take edits and
// that take no new tiles: the + menu offers nothing it would refuse.
func TestFsGridTakesEditsAndNoTiles(t *testing.T) {
	k := newContentClient(t, "pfsgrid", t.TempDir())
	g, err := k.cl.GetGrid(k.ctx, k.landing)
	if err != nil {
		t.Fatal(err)
	}
	if !g.Grid.Writable || g.Grid.AcceptsTiles == nil || g.Grid.GetAcceptsTiles() {
		t.Errorf("fs grid: writable=%v accepts_tiles=%v; want writable, accepting no tiles", g.Grid.Writable, g.Grid.AcceptsTiles)
	}
}

// A clean save lands on disk under the stamp it read, its answer names the
// written bytes' stamp, and the plugin's own Watch echo of the write leaves
// the typed text in place, saved or still being typed.
func TestFsSaveLandsAndItsEchoLeavesTheTypedTextAlone(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.md")
	if err := os.WriteFile(path, []byte("# before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	k := newContentClient(t, "pfssave", root)
	k.fetchGrid(k.landing)
	id := rpc.ContentID(k.tile("notes.md"))
	k.body(id)
	if err := k.cl.SetInterest(k.ctx, []string{k.landing}); err != nil {
		t.Fatal(err)
	}
	k.settle()

	out := outbox.New()
	k.c.PutEditedContent(id, []byte("# typed\n"))
	if err := k.save(out, id); err != nil {
		t.Fatalf("a clean save: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "# typed\n" {
		t.Fatalf("disk holds %q after the save", got)
	}
	if b, _ := k.c.SaveBasis(id); b.Stamp == "" || b.Stamp != k.stampNow(id) {
		t.Errorf("the save basis is %q, the file's stamp %q", b.Stamp, k.stampNow(id))
	}
	if out.Len() != 0 {
		t.Errorf("a landed save left %v parked", out.Keys())
	}

	// The echo arrives as the row's stamp; the body stays.
	k.run(3*time.Second, func() bool {
		if _, ok := k.c.TileContent(id); !ok {
			t.Fatal("the echo of the client's own save dropped the body it had just saved")
		}
		return false
	})
	if k.told[id] == 0 {
		t.Fatal("no echo of the save arrived, so the window tested nothing")
	}
	// The user keeps typing while the next echo is on its way.
	k.c.PutEditedContent(id, []byte("# typed, and more\n"))
	k.run(2*time.Second, func() bool {
		if b, ok := k.c.TileContent(id); !ok || string(b) != "# typed, and more\n" {
			t.Fatalf("typing after the save reads %q, %v", b, ok)
		}
		return false
	})
	if err := k.save(out, id); err != nil {
		t.Fatalf("a second save claiming the first's answer: %v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "# typed, and more\n" {
		t.Errorf("disk holds %q after the second save", got)
	}
}

// Bytes changed on disk under a dirty edit are not clobbered: the save
// claims the stamp the edit was typed over, the plugin refuses it as a
// conflict, and the client's conflict path drops the refused bytes and shows
// the file as it is.
func TestFsSaveOverBytesChangedOnDiskIsRefusedAndSurfaces(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.md")
	if err := os.WriteFile(path, []byte("# mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	k := newContentClient(t, "pfsconflict", root)
	k.fetchGrid(k.landing)
	id := rpc.ContentID(k.tile("notes.md"))
	k.body(id)
	if err := k.cl.SetInterest(k.ctx, []string{k.landing}); err != nil {
		t.Fatal(err)
	}
	k.settle()
	k.c.PutEditedContent(id, []byte("# my edit\n"))
	before := k.tile("notes.md").ContentStamp

	later := time.Now().Add(2 * time.Second)
	if err := os.WriteFile(path, []byte("# theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if !k.run(5*time.Second, func() bool { return k.tile("notes.md").ContentStamp != before }) {
		t.Fatal("the row never learned the file changed")
	}
	if b, _ := k.c.DirtyContent(id); string(b) != "# my edit\n" {
		t.Fatalf("the foreign change dropped the dirty edit: %q", b)
	}

	out := outbox.New()
	err := k.save(out, id)
	if o := clientsync.Of(err); o != clientsync.OutcomeConflict {
		t.Fatalf("the save answered %v (%v), want the conflict a stale version gets", o, err)
	}
	if r := clientsync.ReactSave(clientsync.OutcomeConflict); !r.Log || !r.Refetch {
		t.Errorf("a conflict reacts %+v; it must surface and refetch", r)
	}
	if out.Len() != 0 {
		t.Errorf("a verdict parked %v; only a transport failure parks", out.Keys())
	}
	if got, _ := os.ReadFile(path); string(got) != "# theirs\n" {
		t.Errorf("disk holds %q; the other writer's bytes were overwritten", got)
	}
	if got := k.body(id); string(got) != "# theirs\n" {
		t.Errorf("the tile shows %q after the conflict, want the file as it is", got)
	}
}

// downWrites is a plugin whose writes cannot reach it while down: the answer
// a supervised plugin that died gives.
type downWrites struct {
	namespace.Namespace
	down atomic.Bool
}

func (d *downWrites) WriteContent(ctx context.Context, recv func() (*gridwellv1.WriteContentRequest, error)) (*gridwellv1.TileResponse, error) {
	if d.down.Load() {
		return nil, status.Error(codes.Unavailable, "plugin not responding")
	}
	return d.Namespace.WriteContent(ctx, recv)
}

// A plugin write that fails on transport parks under its key-form id like a
// home write, keeps the typed bytes, and lands when the plugin is back.
func TestFsWriteThePluginCannotTakeParksAndLands(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.md")
	if err := os.WriteFile(path, []byte("# before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var d *downWrites
	k := newContentClientVia(t, "pfspark", root, func(ns namespace.Namespace) namespace.Namespace {
		d = &downWrites{Namespace: ns}
		return d
	})
	k.fetchGrid(k.landing)
	id := rpc.ContentID(k.tile("notes.md"))
	if _, seg, _ := rpc.SplitID(id); rpc.ShapeOf(seg) != rpc.ShapeKey {
		t.Fatalf("%q is not a key-form id; this test is about one", id)
	}
	k.body(id)

	out := outbox.New()
	d.down.Store(true)
	k.c.PutEditedContent(id, []byte("# typed while down\n"))
	err := k.save(out, id)
	if clientsync.Of(err) != clientsync.OutcomeTransport {
		t.Fatalf("a down plugin answered %v, want a transport failure", err)
	}
	if !out.Has(outbox.Key{Op: outbox.OpContent, ID: id}) {
		t.Fatalf("parked %v, want the write under %q", out.Keys(), id)
	}
	if b, _ := k.c.DirtyContent(id); string(b) != "# typed while down\n" {
		t.Fatalf("the typed bytes are %q after the outage", b)
	}

	d.down.Store(false)
	for _, retry := range out.Drain() {
		retry()
	}
	if out.Len() != 0 {
		t.Errorf("the drain left %v parked", out.Keys())
	}
	if got, _ := os.ReadFile(path); string(got) != "# typed while down\n" {
		t.Errorf("disk holds %q after the plugin came back", got)
	}
}
