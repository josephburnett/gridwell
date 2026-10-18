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

// FindHopTimeout bounds one hop's answer during a find fan-out, so a hung
// hop cannot stall the whole find. One owner for both fan-outs: the
// server's per-plugin loop and the connection transport's per-connection one.
const FindHopTimeout = 3 * time.Second

// FindDefaultLimit caps a query that brings no limit, wherever the node
// answers from its own rows.
const FindDefaultLimit = 20

// FindQuery is the parsed form of a Find query, the one grammar every
// plugin reads. Exactly one of ID and Text is set.
type FindQuery struct {
	// ID is an exact tile id to locate; the result carries the tile and its
	// containing-well path.
	ID string
	// Text is interpreted by each plugin against its own data.
	Text string
}

// ParseFindQuery is the selector grammar: id:<tile-id> and free text. New
// selectors extend here, never in a plugin, or the grammar forks.
func ParseFindQuery(q string) FindQuery {
	q = strings.TrimSpace(q)
	if rest, ok := strings.CutPrefix(q, "id:"); ok {
		return FindQuery{ID: strings.TrimSpace(rest)}
	}
	return FindQuery{Text: q}
}

// FindNeedsRow reports whether req filters on a fact only a node's row
// holds, a time or the selection, so an entry with no row passes none.
func FindNeedsRow(req *pb.FindRequest) bool {
	return req.Selected || req.Created != nil || req.Modified != nil || req.ContentChanged != nil
}

// FindFiltered reports whether req narrows by anything beyond its query,
// which is what lets an empty query find.
func FindFiltered(req *pb.FindRequest) bool {
	return req.GridId != "" || req.Kind != "" || FindNeedsRow(req)
}

// FindKinds are the kinds a find's kind filter names, the four
// primitives and the pane tile.
var FindKinds = []string{KindText, KindURL, KindShell, KindWell, KindPane}

// FindKeepsKind reports whether a tile of kind passes req's kind filter.
func FindKeepsKind(req *pb.FindRequest, kind string) bool {
	return req.Kind == "" || req.Kind == kind
}

// CheckFind refuses filters that cannot mean anything, with the reason.
func CheckFind(req *pb.FindRequest) error {
	if req.Recurse && req.GridId == "" {
		return errors.New("find: recurse needs a grid_id")
	}
	if req.Kind != "" && !slices.Contains(FindKinds, req.Kind) {
		return fmt.Errorf("find: kind %q is none of %s", req.Kind, strings.Join(FindKinds, ", "))
	}
	for name, r := range map[string]*pb.TimeRange{
		"created": req.Created, "modified": req.Modified, "content_changed": req.ContentChanged,
	} {
		switch {
		case r == nil:
		case r.From < 0 || r.To < 0:
			return fmt.Errorf("find: %s range has a negative end", name)
		case r.From > 0 && r.To > 0 && r.From > r.To:
			return fmt.Errorf("find: %s range ends before it starts", name)
		}
	}
	return nil
}

// Find issues one query. scope routes to the namespace owning that qualified
// id; "" fans out across every plugin, and transit nodes recurse. limit caps
// results per answering plugin, 0 for its default.
func (c *Client) Find(ctx context.Context, query, scope string, limit int32) (*pb.FindResponse, error) {
	resp, err := c.cl.Find(ctx, connect.NewRequest(&pb.FindRequest{
		Query: query, Scope: scope, Limit: limit,
	}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

// FindHop is one fan-out hop's answer as the fan-out keeps it: what the hop
// found, or for a hop that failed, one skip naming the hop itself, an empty
// namespace its qualifier fills in. Unimplemented is no results.
func FindHop(resp *pb.FindResponse, err error) *pb.FindResponse {
	var reason string
	switch {
	case err == nil:
		return resp
	case status.Code(err) == codes.Unimplemented:
		return &pb.FindResponse{}
	case errors.Is(err, context.DeadlineExceeded) || status.Code(err) == codes.DeadlineExceeded:
		reason = "did not answer within " + FindHopTimeout.String()
	default:
		reason = status.Convert(err).Message()
	}
	return &pb.FindResponse{Skipped: []*pb.FindSkip{{Reason: reason}}}
}
