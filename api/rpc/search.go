package rpc

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// SearchHopTimeout bounds one hop's answer during a search fan-out, so a hung
// hop cannot stall the whole search. One owner for both fan-outs: the
// server's per-plugin loop and the connection transport's per-connection one.
const SearchHopTimeout = 3 * time.Second

// SearchDefaultLimit caps a query that brings no limit, wherever the node
// answers from its own rows.
const SearchDefaultLimit = 20

// SearchQuery is the parsed form of a Search query, the one grammar every
// plugin reads. Exactly one of ID and Text is set.
type SearchQuery struct {
	// ID is an exact tile id to locate; the result carries the tile and its
	// containing-well path.
	ID string
	// Text is interpreted by each plugin against its own data.
	Text string
}

// ParseSearchQuery is the selector grammar: id:<tile-id> and free text. New
// selectors extend here, never in a plugin, or the grammar forks.
func ParseSearchQuery(q string) SearchQuery {
	q = strings.TrimSpace(q)
	if rest, ok := strings.CutPrefix(q, "id:"); ok {
		return SearchQuery{ID: strings.TrimSpace(rest)}
	}
	return SearchQuery{Text: q}
}

// SearchNeedsRow reports whether req filters on a fact only a node's row
// holds, a time or the selection, so an entry with no row passes none.
func SearchNeedsRow(req *pb.SearchRequest) bool {
	return req.Selected || req.Created != nil || req.Modified != nil || req.ContentChanged != nil
}

// SearchFiltered reports whether req narrows by anything beyond its query,
// which is what lets an empty query search.
func SearchFiltered(req *pb.SearchRequest) bool {
	return req.GridId != "" || req.Kind != "" || SearchNeedsRow(req)
}

// SearchKinds are the kinds a search's kind filter names, the four
// primitives and the pane tile.
var SearchKinds = []string{KindText, KindURL, KindShell, KindWell, KindPane}

// SearchKeepsKind reports whether a tile of kind passes req's kind filter.
func SearchKeepsKind(req *pb.SearchRequest, kind string) bool {
	return req.Kind == "" || req.Kind == kind
}

// CheckSearch refuses filters that cannot mean anything, with the reason.
func CheckSearch(req *pb.SearchRequest) error {
	if req.Recurse && req.GridId == "" {
		return errors.New("search: recurse needs a grid_id")
	}
	if req.Kind != "" && !slices.Contains(SearchKinds, req.Kind) {
		return fmt.Errorf("search: kind %q is none of %s", req.Kind, strings.Join(SearchKinds, ", "))
	}
	for name, r := range map[string]*pb.TimeRange{
		"created": req.Created, "modified": req.Modified, "content_changed": req.ContentChanged,
	} {
		switch {
		case r == nil:
		case r.From < 0 || r.To < 0:
			return fmt.Errorf("search: %s range has a negative end", name)
		case r.From > 0 && r.To > 0 && r.From > r.To:
			return fmt.Errorf("search: %s range ends before it starts", name)
		}
	}
	return nil
}

// Search issues one query. scope routes to the namespace owning that qualified
// id; "" fans out across every plugin, and transit nodes recurse. limit caps
// results per answering plugin, 0 for its default.
func (c *Client) Search(ctx context.Context, query, scope string, limit int32) (*pb.SearchResponse, error) {
	resp, err := c.cl.Search(ctx, connect.NewRequest(&pb.SearchRequest{
		Query: query, Scope: scope, Limit: limit,
	}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// SearchHop is one fan-out hop's answer as the fan-out keeps it: what the hop
// found, or for a hop that failed, one skip naming the hop itself, an empty
// namespace its qualifier fills in. Unimplemented is no results.
func SearchHop(resp *pb.SearchResponse, err error) *pb.SearchResponse {
	var reason string
	switch {
	case err == nil:
		return resp
	case status.Code(err) == codes.Unimplemented:
		return &pb.SearchResponse{}
	case errors.Is(err, context.DeadlineExceeded) || status.Code(err) == codes.DeadlineExceeded:
		reason = "did not answer within " + SearchHopTimeout.String()
	default:
		reason = status.Convert(err).Message()
	}
	return &pb.SearchResponse{Skipped: []*pb.SearchSkip{{Reason: reason}}}
}
