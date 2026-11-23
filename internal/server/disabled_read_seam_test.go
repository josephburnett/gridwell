package server_test

import (
	"context"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/clientsync"
	"github.com/josephburnett/gridwell/internal/connection"
	"github.com/josephburnett/gridwell/internal/sourcecache"
)

// A body the node does not hold, read from a connection the user disabled,
// answers the disabled verdict through the real transport, the cache in
// front of it and the web API, alone and in a list, so the client reads it
// as OutcomeDisabled, which tells nothing (clientsync.ReadSurfaces), and not
// as an outage, which goes on the notice row.
func TestAReadOfADisabledConnectionAnswersItsVerdict(t *testing.T) {
	m := newMesh(t, []string{"anode01", "bnode01"}, []meshEdge{{From: "anode01", Name: "second", To: "bnode01"}})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	far := m.home("anode01", "bnode01", "second")
	held := m.text(ctx, "anode01", far, 0, "held words")
	note := m.text(ctx, "anode01", far, 1, "far words")

	a := m.nodes["anode01"]
	transport := transportOf(a.reg).(*connection.Server)
	cache, err := sourcecache.Open(t.TempDir() + "/cache.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	restart(a.reg, cache.Front(transport, sourcecache.Options{}))
	if _, _, _, err := a.cl.ReadContent(ctx, held.Id); err != nil {
		t.Fatal(err)
	}
	if err := transport.Disable("second"); err != nil {
		t.Fatal(err)
	}

	// It reads as dark: what the node holds is still served.
	if data, _, _, err := a.cl.ReadContent(ctx, held.Id); err != nil || string(data) != "held words" {
		t.Errorf("a held body: %q, %v; want it served", data, err)
	}
	_, _, _, err = a.cl.ReadContent(ctx, note.Id)
	if o := clientsync.Of(err); o != clientsync.OutcomeDisabled {
		t.Errorf("a read alone: outcome %v (%v), want OutcomeDisabled", o, err)
	}
	a.cl.ReadContents(ctx, []string{note.Id}, func(_ string, _ []byte, _ string, _ rpc.ContentBasis, err error) {
		if o := clientsync.Of(err); o != clientsync.OutcomeDisabled {
			t.Errorf("a read in a list: outcome %v (%v), want OutcomeDisabled", o, err)
		}
	})
}
