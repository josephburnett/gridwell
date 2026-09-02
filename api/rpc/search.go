package rpc

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// SearchHopTimeout bounds one hop's answer during a search fan-out, so a
// hung hop — a dead ssh tunnel, say — cannot stall the whole search. It is
// the one owner for both fan-outs: the server's per-plugin loop and the
// connection transport's per-connection loop.
const SearchHopTimeout = 3 * time.Second

// SearchQuery is the parsed form of a Search query: the one grammar every
// plugin reads, so id: means the same thing in every namespace. Exactly one
// of ID and Text is set.
type SearchQuery struct {
	// ID is an exact tile id to locate ("" for a text query). The result
	// carries the tile and its containing-well path.
	ID string
	// Text is the free-text query, interpreted by each plugin against its
	// own data (names, bodies, addresses, file names…).
	Text string
}

// ParseSearchQuery parses the selector grammar. It is deliberately tiny:
// id:<tile-id> and free text. New selectors extend here, never in a plugin,
// or the grammar forks per plugin.
func ParseSearchQuery(q string) SearchQuery {
	q = strings.TrimSpace(q)
	if rest, ok := strings.CutPrefix(q, "id:"); ok {
		return SearchQuery{ID: strings.TrimSpace(rest)}
	}
	return SearchQuery{Text: q}
}

// SearchResult is one hit: a place, not just a row — the tile plus its
// containing-well chain from the plugin root. It and its conversions are
// generated from the proto into wire_gen.go, like every other record.

// Search issues one query against the server surface. scope routes to the
// namespace owning that qualified id; "" fans out across every configured
// plugin, and transit nodes recurse. limit caps results per answering
// plugin; 0 means the plugin's default.
func (c *Client) Search(ctx context.Context, query, scope string, limit int32) ([]SearchResult, error) {
	resp, err := c.cl.Search(ctx, connect.NewRequest(&pb.SearchRequest{
		Query: query, Scope: scope, Limit: limit,
	}))
	if err != nil {
		return nil, err
	}
	return SearchResultsFromProto(resp.Msg.Results), nil
}
