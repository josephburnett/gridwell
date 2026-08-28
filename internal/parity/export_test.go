package parity

import (
	"context"

	"github.com/josephburnett/gridwell/api/rpc"
)

// Crawl walks one node into a Snapshot — the tests' single-node sugar over
// crawl (determinism, the MaxGrids abort). The CLI ships only CrawlPair.
func Crawl(ctx context.Context, cl *rpc.Client, o Options) (*Snapshot, error) {
	snaps, err := crawl(ctx, []*rpc.Client{cl}, o)
	if err != nil {
		return nil, err
	}
	return snaps[0], nil
}
