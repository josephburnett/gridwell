package sourcecache

// prefetchPause is a promise about time — the walk yields the link between
// every RPC — so a walk's cost is derived from it: N grids cannot be warmed in
// less than N pauses.

import (
	"context"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/internal/namespace"
)

// setPrefetchPause lengthens the walk's gap for one test and restores it.
func setPrefetchPause(t *testing.T, d time.Duration) time.Duration {
	t.Helper()
	was := prefetchPause
	t.Cleanup(func() { prefetchPause = was })
	prefetchPause = d
	return prefetchPause
}

// distinct is how many grids the walk asked the far node for.
func (g *gridReads) distinct() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.n)
}

// A walk of N grids takes at least N pauses. Without the gap the crawl is a
// tight loop against the far node, which is what the user's own reads are
// competing with on the link this cache exists for.
func TestPrefetchPausesBetweenEveryRead(t *testing.T) {
	pause := setPrefetchPause(t, 30*time.Millisecond)
	var reads *gridReads
	cc, far, farRoot, _ := connFixtureWith(t, Options{Prefetch: true},
		func(ns namespace.Namespace) namespace.Namespace {
			reads = &gridReads{Namespace: ns, n: map[string]int{}}
			return reads
		})
	seedNested(t, far.Namespace, farRoot)

	start := time.Now()
	cc.prefetch(context.Background(), "")
	took := time.Since(start)

	grids := reads.distinct()
	if grids < 3 {
		t.Fatalf("the walk read %d grids, want the far root, the nested grid and the inner one", grids)
	}
	if floor := time.Duration(grids) * pause; took < floor {
		t.Fatalf("a walk of %d grids took %v, under %d pauses (%v) — the walk must yield the link between reads", grids, took, grids, floor)
	}
}
