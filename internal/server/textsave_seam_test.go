package server

import (
	"context"
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

	target, err := cl.CreateText(ctx, &rpc.CreateTextRequest{
		GridID: root, X: 0, Y: 0, W: 1, H: 1, Data: []byte("v0"),
	})
	if err != nil {
		t.Fatal(err)
	}
	link, err := cl.CreateLeafLink(ctx, &rpc.CreateLeafLinkRequest{
		GridID: root, X: 2, Y: 0, W: 1, H: 1,
		Kind: rpc.KindText, LinkTargetID: target.ID, Label: "linked",
	})
	if err != nil {
		t.Fatal(err)
	}
	if link.ID == target.ID || link.ContentID() != target.ID {
		t.Fatalf("link %s content id %s, want a distinct row owning %s",
			link.ID, link.ContentID(), target.ID)
	}

	// The client as it stands when the user has typed into the document
	// through the link: one content entry, keyed by the id that owns the
	// bytes, dirty, based on the version it was fetched under.
	c := cache.New()
	c.PutFetchedContent(target.ID, []byte("v0"), target.Version)
	c.PutEditedContent(target.ID, []byte("typed"))

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
			basis, haveBasis := c.SaveBasis(target.ID)
			claim := textedit.SaveClaim(rowID == target.ID, rowVersion, basis, haveBasis)
			tile, err := cl.WriteContent(ctx, target.ID, claim, data)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			c.PutSavedContent(tile.ID, data, tile.Version)
		}
	}

	q := textedit.NewSaveQueue()
	// The ascent flush: it holds the link row it was descended through.
	q.Enqueue(textedit.SaveQueueKey(link.ID, link.ContentID()),
		save(link.ID, link.Version, []byte("typed")))
	// The debounce sweep: it holds the content id.
	q.Enqueue(textedit.SaveQueueKey(target.ID, target.ID),
		save(target.ID, target.Version, []byte("typed and more")))
	wg.Wait()

	if len(errs) > 0 {
		t.Fatalf("the client conflicted with itself: %v — the two flush paths of one "+
			"document ran on different save chains", errs)
	}
	data, _, _, err := cl.ReadContent(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "typed and more" {
		t.Errorf("stored content = %q, want the last write in queue order", data)
	}
	if basis, ok := c.SaveBasis(target.ID); !ok || basis != target.Version+2 {
		t.Errorf("save basis = %d (present %v), want %d: both writes chained",
			basis, ok, target.Version+2)
	}
}
