package server

import (
	"context"
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"sync"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/cache"
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
	c.PutFetchedContent(target.Id, []byte("v0"), target.Version, c.AskContent(target.Id))
	c.PutEditedContent(target.Id, []byte("typed"))

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
	// One flush, spelled as the client spells it: claim at send time through
	// textedit.SaveClaim, write, then advance the basis from the response.
	save := func(rowID string, rowVersion int64, data []byte) func() {
		return func() {
			defer wg.Done()
			rendezvous()
			basis, haveBasis := c.SaveBasis(target.Id)
			claim := textedit.SaveClaim(rowID == target.Id, rowVersion, basis, haveBasis)
			tile, err := cl.WriteContent(ctx, target.Id, claim, data)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			c.PutSavedContent(tile, data)
		}
	}

	q := textedit.NewSaveQueue()
	// The ascent flush: it holds the link row it was descended through.
	q.Enqueue(textedit.SaveQueueKey(link.Id, rpc.ContentID(link)),
		save(link.Id, link.Version, []byte("typed")))
	// The debounce sweep: it holds the content id.
	q.Enqueue(textedit.SaveQueueKey(target.Id, target.Id),
		save(target.Id, target.Version, []byte("typed and more")))
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
		t.Errorf("stored content = %q, want the last write in queue order", data)
	}
	if basis, ok := c.SaveBasis(target.Id); !ok || basis != target.Version+2 {
		t.Errorf("save basis = %d (present %v), want %d: both writes chained",
			basis, ok, target.Version+2)
	}
}
