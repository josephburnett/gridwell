package server

import (
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"sync"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cache"
	"github.com/josephburnett/gridwell/client/clientsync"
	"github.com/josephburnett/gridwell/client/outbox"
	"github.com/josephburnett/gridwell/client/textedit"
)

// The text-save seam: the client's save queue and claim rule against the real
// store's version rule, over the real Connect handler. A unit test on either
// side cannot see this — the queue serializes tasks correctly for whatever key
// it is handed, and the store refuses a stale claim correctly — and the bug is
// the client handing one document two keys.

// TestLinkedDocumentFlushesShareOneChain: a leaf link and its target are one
// document, and the two flush paths hold different ids for it — the ascent
// flush holds the viewed row (the LINK row), the debounce sweep holds the
// content id. On two chains they run concurrently, both read the same
// SaveBasis, both claim it, and the store refuses the loser: the client
// conflicting with itself over an edit the user made once.
func TestLinkedDocumentFlushesShareOneChain(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()

	target, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}}, []byte("v0"))
	if err != nil {
		t.Fatal(err)
	}
	link, err := cl.CreateTile(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 2, Y: 0, W: 1, H: 1, LinkTargetId: target.Id, AltText: "linked"}})
	if err != nil {
		t.Fatal(err)
	}
	if link.Id == target.Id || rpc.ContentID(link) != target.Id {
		t.Fatalf("link %s content id %s, want a distinct row owning %s",
			link.Id, rpc.ContentID(link), target.Id)
	}

	// The client as it stands when the user has typed into the document
	// through the link: one content entry, keyed by the id that owns the
	// bytes, dirty, based on the version it was fetched under.
	c := cache.New()
	c.PutFetchedContent(target.Id, []byte("v0"), rpc.BasisOf(target), c.AskContent(target.Id))
	c.PutEditedContent(target.Id, []byte("typed and more"))

	// Both flushes reach for the head of the same chain at the same moment.
	// Serialized, the second waits out this window and then reads the basis
	// the first established; on two chains they proceed together.
	var mu sync.Mutex
	arrived := 0
	var errs []error
	rendezvous := func() {
		mu.Lock()
		arrived++
		mu.Unlock()
		deadline := time.Now().Add(250 * time.Millisecond)
		for time.Now().Before(deadline) {
			mu.Lock()
			n := arrived
			mu.Unlock()
			if n == 2 {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	// One flush, spelled as the client spells it: what the entry owes and its
	// claim at send time through textedit.SaveNow, write, then advance the
	// basis from the response.
	save := func() func() bool {
		return func() bool {
			defer wg.Done()
			rendezvous()
			data, claim, send := textedit.SaveNow(c, target.Id)
			if !send {
				return true
			}
			tile, err := cl.WriteContent(ctx, target.Id, claim, data)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return false
			}
			c.PutSavedContent(tile, data)
			return true
		}
	}

	q := outbox.NewSaveQueue()
	// The ascent flush: it holds the link row it was descended through.
	q.Enqueue(textedit.SaveQueueKey(link.Id, rpc.ContentID(link)), save())
	// The debounce sweep: it holds the content id.
	q.Enqueue(textedit.SaveQueueKey(target.Id, target.Id), save())
	wg.Wait()

	if len(errs) > 0 {
		t.Fatalf("the client conflicted with itself: %v — the two flush paths of one "+
			"document ran on different save chains", errs)
	}
	data, _, _, err := cl.ReadContent(ctx, target.Id)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "typed and more" {
		t.Errorf("stored content = %q, want the bytes typed", data)
	}
	if basis, ok := c.SaveBasis(target.Id); !ok || basis.Version != target.Version+1 {
		t.Errorf("save basis = %d (present %v), want %d: the second flush, behind the first on its chain, found nothing owed",
			basis.Version, ok, target.Version+1)
	}
}

// TestASaveBehindARefusedOneSendsNothing: an agent writes the document while
// the user's edit is unsaved, so the cached row moves on but the entry keeps
// the base it was read at. The first save is refused, and its reconcile takes
// the entry down for the row on disk; the save queued behind it, holding the
// typing that followed, must not then claim the row's new version and land
// over the agent's words.
func TestASaveBehindARefusedOneSendsNothing(t *testing.T) {
	_, cl, root := newTestServer(t)
	ctx := context.Background()
	doc, err := cl.CreateWithContent(ctx, &gridwellv1.CreateTileRequest{GridId: root, Tile: &gridwellv1.Tile{Kind: rpc.KindText, X: 0, Y: 0, W: 1, H: 1}}, []byte("v0"))
	if err != nil {
		t.Fatal(err)
	}
	c := cache.New()
	c.PutFetchedContent(doc.Id, []byte("v0"), rpc.BasisOf(doc), c.AskContent(doc.Id))
	c.PutEditedContent(doc.Id, []byte("typed"))
	if _, err := cl.WriteContent(ctx, doc.Id, rpc.BasisOf(doc), []byte("agent")); err != nil {
		t.Fatal(err)
	}

	sent := 0
	save := func(inFlight func()) func() bool {
		return func() bool {
			data, claim, send := textedit.SaveNow(c, doc.Id)
			if !send {
				return true
			}
			sent++
			inFlight()
			tile, err := cl.WriteContent(ctx, doc.Id, claim, data)
			if err != nil {
				if clientsync.ReactSave(clientsync.Of(err)).DropLocal {
					c.DropTileContent(doc.Id)
				}
				return false
			}
			c.PutSavedContent(tile, data)
			return true
		}
	}
	done := make(chan struct{})
	q := outbox.NewSaveQueue()
	key := textedit.SaveQueueKey(doc.Id, doc.Id)
	// Typed while the first save is out, and swept onto the chain behind it.
	q.Enqueue(key, save(func() { c.PutEditedContent(doc.Id, []byte("typed and more")) }))
	q.Enqueue(key, save(func() {}))
	q.After(key, func(bool) { close(done) })
	<-done

	data, _, _, err := cl.ReadContent(ctx, doc.Id)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "agent" || sent != 1 {
		t.Errorf("the node holds %q after %d sends, want the agent's words after the one refused", data, sent)
	}
}
