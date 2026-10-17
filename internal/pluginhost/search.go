package pluginhost

// Search on a plugin's namespace. The plugin answers text; the filters are
// the node's, applied to the rows the answer maps to (store.tileFilter), so
// the plugin door carries none.

import (
	"context"
	"slices"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// searchHit is one placed hit and the row behind it, 0 for an entry with none.
type searchHit struct {
	result *gridwellv1.SearchResult
	row    int64
}

// Search turns each hit into a place (tile plus well chain) through the
// synthesis GetGrid runs, dropping hits it cannot place, and keeps the ones
// the filters pass. An empty query with a filter lists instead: the grid and,
// with recurse, every grid under it, or with no grid the rows whose facts
// pass. An id: locate is refused: there is no parent index here.
func (a *Adapter) Search(ctx context.Context, req *gridwellv1.SearchRequest) (*gridwellv1.SearchResponse, error) {
	if q := rpc.ParseSearchQuery(req.Query); q.ID != "" {
		return nil, status.Error(codes.Unimplemented, "plugin: locate by id is not supported (no parent index in the memory DB)")
	}
	g := &searchGrids{a: a, ctx: ctx, byContext: map[string]*synthesized{}}
	scope := ""
	if req.GridId != "" {
		_, ckey, err := a.resolveGrid(req.GridId)
		if err != nil {
			return nil, err
		}
		scope = ckey
	}
	listing := strings.TrimSpace(req.Query) == ""
	var (
		hits []searchHit
		err  error
	)
	switch {
	case listing && !rpc.SearchFiltered(req):
		return &gridwellv1.SearchResponse{}, nil
	case listing && scope != "":
		// A scope no held way reaches lists with paths that start at it.
		var above []*gridwellv1.Tile
		if above, _, err = g.chain(scope); err == nil {
			hits, err = g.walk(scope, above, req.Recurse, map[string]bool{})
		}
	case listing:
		hits, err = g.rows(req)
	default:
		hits, err = g.text(req, scope)
	}
	if err != nil {
		return nil, err
	}
	return a.filterHits(req, hits, listing)
}

// filterHits keeps the hits whose rows pass the filters; a listing is ordered
// newest first and capped, as home's is.
func (a *Adapter) filterHits(req *gridwellv1.SearchRequest, hits []searchHit, listing bool) (*gridwellv1.SearchResponse, error) {
	out := &gridwellv1.SearchResponse{}
	if !listing && !rpc.SearchNeedsRow(req) {
		for _, h := range hits {
			out.Results = append(out.Results, h.result)
		}
		return out, nil
	}
	var ids []int64
	for _, h := range hits {
		if h.row != 0 {
			ids = append(ids, h.row)
		}
	}
	passing, err := a.mem.SearchPassing(ids, req)
	if err != nil {
		return nil, err
	}
	kept := hits[:0]
	for _, h := range hits {
		_, ok := passing[h.row]
		if ok || (h.row == 0 && !rpc.SearchNeedsRow(req)) {
			kept = append(kept, h)
		}
	}
	if listing {
		// An entry with no row has no time and lists after every row.
		slices.SortStableFunc(kept, func(x, y searchHit) int { return int(passing[y.row] - passing[x.row]) })
		limit := int(req.Limit)
		if limit <= 0 {
			limit = rpc.SearchDefaultLimit
		}
		kept = kept[:min(len(kept), limit)]
	}
	for _, h := range kept {
		out.Results = append(out.Results, h.result)
	}
	return out, nil
}

// searchGrids synthesizes each grid a search reads once.
type searchGrids struct {
	a         *Adapter
	ctx       context.Context
	byContext map[string]*synthesized
}

func (g *searchGrids) grid(ckey string) (*synthesized, error) {
	if s, ok := g.byContext[ckey]; ok {
		return s, nil
	}
	s, err := g.a.synthesize(g.ctx, rpc.EntryGridID(ckey))
	if err != nil {
		return nil, err
	}
	g.byContext[ckey] = s
	return s, nil
}

// hit is key's tile on the grid of ckey, nil when the grid has none.
func (g *searchGrids) hit(ckey, key string, path []*gridwellv1.Tile, snippet string, score float64) (*searchHit, error) {
	s, err := g.grid(ckey)
	if err != nil {
		return nil, err
	}
	for i, row := range s.rows {
		if row.Key == key {
			return &searchHit{row: row.ID, result: &gridwellv1.SearchResult{
				Tile: s.tiles[i], Path: path, Snippet: snippet, Score: score}}, nil
		}
	}
	return nil, nil
}

// text is the plugin's answer to the query, placed, and kept when it sits on
// the scope (or under it, with recurse): a hit's context path names every grid
// above it.
func (g *searchGrids) text(req *gridwellv1.SearchRequest, scope string) ([]searchHit, error) {
	resp, err := ask(&g.a.lacks, "Search", func() (*pluginv1.SearchResponse, error) {
		return g.a.cp.Search(g.ctx, &pluginv1.SearchRequest{Query: req.Query, Limit: req.Limit})
	})
	if err != nil {
		return nil, err
	}
	var out []searchHit
	for _, r := range resp.Results {
		n := len(r.ContextPath)
		if r.Entry == nil || n == 0 {
			continue
		}
		leaf := r.ContextPath[n-1]
		if scope != "" && leaf != scope && !(req.Recurse && slices.Contains(r.ContextPath, scope)) {
			continue
		}
		var path []*gridwellv1.Tile
		placed := true
		for i := 1; i < n; i++ {
			parent, err := g.grid(r.ContextPath[i-1])
			if err != nil {
				return nil, err
			}
			well := parent.tileOpening(rpc.EntryGridID(r.ContextPath[i]))
			if well == nil {
				placed = false
				break
			}
			path = append(path, well)
		}
		if !placed {
			continue
		}
		h, err := g.hit(leaf, r.Entry.Key, path, r.Snippet, r.Score)
		if err != nil {
			return nil, err
		}
		if h != nil {
			out = append(out, *h)
		}
	}
	return out, nil
}

// walk lists the grid of ckey and, with recurse, every grid its wells open,
// each tile placed under the wells that reached it; path is the chain above
// ckey.
func (g *searchGrids) walk(ckey string, path []*gridwellv1.Tile, recurse bool, seen map[string]bool) ([]searchHit, error) {
	seen[ckey] = true
	s, err := g.grid(ckey)
	if err != nil {
		return nil, err
	}
	var out []searchHit
	for i, t := range s.tiles {
		out = append(out, searchHit{row: s.rows[i].ID, result: &gridwellv1.SearchResult{Tile: t, Path: path, Score: 1}})
	}
	if !recurse {
		return out, nil
	}
	for _, t := range s.tiles {
		child, _, isTile, ok := rpc.SplitEntryID(t.ChildGridId)
		if t.ChildGridId == "" || !ok || isTile || seen[child] {
			continue
		}
		under, err := g.walk(child, append(slices.Clone(path), t), true, seen)
		if err != nil {
			return nil, err
		}
		out = append(out, under...)
	}
	return out, nil
}

// rows is an empty query with no grid: the rows whose facts pass, each placed
// on its chain, and dropped, as an unplaceable text hit is, without one.
func (g *searchGrids) rows(req *gridwellv1.SearchRequest) ([]searchHit, error) {
	limit := int(req.Limit)
	if limit <= 0 {
		limit = rpc.SearchDefaultLimit
	}
	found, err := g.a.mem.SearchRows(req, limit)
	if err != nil {
		return nil, err
	}
	var out []searchHit
	for _, r := range found {
		ckey, err := g.a.mem.ContextKey(r.GridID)
		if err != nil {
			return nil, err
		}
		path, ok, err := g.chain(ckey)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		h, err := g.hit(ckey, r.Key, path, "", 1)
		if err != nil {
			return nil, err
		}
		if h != nil {
			out = append(out, *h)
		}
	}
	return out, nil
}

// chain is the wells from a collection down to the grid of target, found by
// listing down from each collection the plugin declares through the grids the
// node holds rows for, which are the ones the user has touched: the search
// never walks the source. ok is false when no such way down reaches it.
func (g *searchGrids) chain(target string) (path []*gridwellv1.Tile, ok bool, err error) {
	ci, err := g.a.cp.Info(g.ctx, &pluginv1.InfoRequest{})
	if err != nil {
		return nil, false, err
	}
	type step struct {
		ckey string
		path []*gridwellv1.Tile
	}
	var queue []step
	seen := map[string]bool{}
	for _, m := range declaredEntries(ci) {
		if m.Context != "" && !seen[m.Context] {
			seen[m.Context] = true
			queue = append(queue, step{ckey: m.Context})
		}
	}
	for len(queue) > 0 {
		at := queue[0]
		queue = queue[1:]
		if at.ckey == target {
			return at.path, true, nil
		}
		s, err := g.grid(at.ckey)
		if err != nil {
			return nil, false, err
		}
		for _, t := range s.tiles {
			child, _, isTile, ok := rpc.SplitEntryID(t.ChildGridId)
			if t.ChildGridId == "" || !ok || isTile || seen[child] {
				continue
			}
			if child != target {
				if _, held, err := g.a.mem.LookupContext(child); err != nil || !held {
					if err != nil {
						return nil, false, err
					}
					continue
				}
			}
			seen[child] = true
			queue = append(queue, step{ckey: child, path: append(slices.Clone(at.path), t)})
		}
	}
	return nil, false, nil
}
