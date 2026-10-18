package pluginhost

// Find on a plugin's namespace. The plugin answers text; the filters are
// the node's, applied to the rows the answer maps to (store.tileFilter), so
// the plugin API carries none.

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

// findHit is one placed hit and the row behind it, 0 for an entry with none.
type findHit struct {
	result *gridwellv1.FindResult
	row    int64
}

// Find turns each hit into a place (tile plus door chain) through the
// synthesis GetGrid runs, dropping hits it cannot place, and keeps the ones
// the filters pass. An empty query with a filter lists instead: the grid and,
// with recurse, every grid under it, or with no grid the rows whose facts
// pass. An id: locate is refused: there is no parent index here.
func (a *Adapter) Find(ctx context.Context, req *gridwellv1.FindRequest) (*gridwellv1.FindResponse, error) {
	if q := rpc.ParseFindQuery(req.Query); q.ID != "" {
		return nil, status.Error(codes.Unimplemented, "plugin: locate by id is not supported (no parent index in the memory DB)")
	}
	g := &findGrids{a: a, ctx: ctx, byGrid: map[string]*synthesized{}}
	scope := ""
	if req.GridId != "" {
		_, gkey, err := a.resolveGrid(req.GridId)
		if err != nil {
			return nil, err
		}
		scope = gkey
	}
	listing := strings.TrimSpace(req.Query) == ""
	var (
		hits []findHit
		err  error
	)
	switch {
	case listing && !rpc.FindFiltered(req):
		return &gridwellv1.FindResponse{}, nil
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

// filterHits keeps the hits whose rows pass the filters, and the kind a hit
// with no row answers with; a listing is ordered newest first and capped, as
// home's is.
func (a *Adapter) filterHits(req *gridwellv1.FindRequest, hits []findHit, listing bool) (*gridwellv1.FindResponse, error) {
	out := &gridwellv1.FindResponse{}
	hits = slices.DeleteFunc(hits, func(h findHit) bool { return !rpc.FindKeepsKind(req, h.result.GetTile().GetKind()) })
	if !listing && !rpc.FindNeedsRow(req) {
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
	passing, err := a.mem.FindPassing(ids, req)
	if err != nil {
		return nil, err
	}
	kept := hits[:0]
	for _, h := range hits {
		_, ok := passing[h.row]
		if ok || (h.row == 0 && !rpc.FindNeedsRow(req)) {
			kept = append(kept, h)
		}
	}
	if listing {
		// An entry with no row has no time and lists after every row.
		slices.SortStableFunc(kept, func(x, y findHit) int { return int(passing[y.row] - passing[x.row]) })
		limit := int(req.Limit)
		if limit <= 0 {
			limit = rpc.FindDefaultLimit
		}
		kept = kept[:min(len(kept), limit)]
	}
	for _, h := range kept {
		out.Results = append(out.Results, h.result)
	}
	return out, nil
}

// findGrids synthesizes each grid a find reads once.
type findGrids struct {
	a      *Adapter
	ctx    context.Context
	byGrid map[string]*synthesized
}

func (g *findGrids) grid(gkey string) (*synthesized, error) {
	if s, ok := g.byGrid[gkey]; ok {
		return s, nil
	}
	s, err := g.a.synthesize(g.ctx, rpc.EntryGridID(gkey))
	if err != nil {
		return nil, err
	}
	g.byGrid[gkey] = s
	return s, nil
}

// hit is key's tile on the grid of gkey, nil when the grid has none.
func (g *findGrids) hit(gkey, key string, path []*gridwellv1.Tile, snippet string, score float64) (*findHit, error) {
	s, err := g.grid(gkey)
	if err != nil {
		return nil, err
	}
	for i, row := range s.rows {
		if row.Key == key {
			return &findHit{row: row.ID, result: &gridwellv1.FindResult{
				Tile: s.tiles[i], Path: path, Snippet: snippet, Score: score}}, nil
		}
	}
	return nil, nil
}

// text is the plugin's answer to the query, placed, and kept when it sits on
// the scope (or under it, with recurse): a hit's grid path names every grid
// above it.
func (g *findGrids) text(req *gridwellv1.FindRequest, scope string) ([]findHit, error) {
	resp, err := ask(&g.a.lacks, "Find", func() (*pluginv1.FindResponse, error) {
		return g.a.cp.Find(g.ctx, &pluginv1.FindRequest{Query: req.Query, Limit: req.Limit})
	})
	if err != nil {
		return nil, err
	}
	var out []findHit
	for _, r := range resp.Results {
		n := len(r.GridPath)
		if r.Entry == nil || n == 0 {
			continue
		}
		leaf := r.GridPath[n-1]
		if scope != "" && leaf != scope && !(req.Recurse && slices.Contains(r.GridPath, scope)) {
			continue
		}
		var path []*gridwellv1.Tile
		placed := true
		for i := 1; i < n; i++ {
			parent, err := g.grid(r.GridPath[i-1])
			if err != nil {
				return nil, err
			}
			door := parent.tileOpening(rpc.EntryGridID(r.GridPath[i]))
			if door == nil {
				placed = false
				break
			}
			path = append(path, door)
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

// walk lists the grid of gkey and, with recurse, every grid its doors open,
// each tile placed under the doors that reached it; path is the chain above
// gkey.
func (g *findGrids) walk(gkey string, path []*gridwellv1.Tile, recurse bool, seen map[string]bool) ([]findHit, error) {
	seen[gkey] = true
	s, err := g.grid(gkey)
	if err != nil {
		return nil, err
	}
	var out []findHit
	for i, t := range s.tiles {
		out = append(out, findHit{row: s.rows[i].ID, result: &gridwellv1.FindResult{Tile: t, Path: path, Score: 1}})
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
func (g *findGrids) rows(req *gridwellv1.FindRequest) ([]findHit, error) {
	limit := int(req.Limit)
	if limit <= 0 {
		limit = rpc.FindDefaultLimit
	}
	found, err := g.a.mem.FindRows(req, limit)
	if err != nil {
		return nil, err
	}
	var out []findHit
	for _, r := range found {
		gkey, err := g.a.mem.GridKey(r.GridID)
		if err != nil {
			return nil, err
		}
		path, ok, err := g.chain(gkey)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		h, err := g.hit(gkey, r.Key, path, "", 1)
		if err != nil {
			return nil, err
		}
		if h != nil {
			out = append(out, *h)
		}
	}
	return out, nil
}

// chain is the doors from a grid down to the grid of target, found by
// listing down from each grid the plugin declares through the grids the
// node holds rows for, which are the ones the user has touched: the find
// never walks the source. ok is false when no such way down reaches it.
func (g *findGrids) chain(target string) (path []*gridwellv1.Tile, ok bool, err error) {
	ci, err := g.a.cp.Info(g.ctx, &pluginv1.InfoRequest{})
	if err != nil {
		return nil, false, err
	}
	type step struct {
		gkey string
		path []*gridwellv1.Tile
	}
	var queue []step
	seen := map[string]bool{}
	for _, m := range declaredGrids(ci) {
		if m.Grid != "" && !seen[m.Grid] {
			seen[m.Grid] = true
			queue = append(queue, step{gkey: m.Grid})
		}
	}
	for len(queue) > 0 {
		at := queue[0]
		queue = queue[1:]
		if at.gkey == target {
			return at.path, true, nil
		}
		s, err := g.grid(at.gkey)
		if err != nil {
			return nil, false, err
		}
		for _, t := range s.tiles {
			child, _, isTile, ok := rpc.SplitEntryID(t.ChildGridId)
			if t.ChildGridId == "" || !ok || isTile || seen[child] {
				continue
			}
			if child != target {
				if _, held, err := g.a.mem.LookupGrid(child); err != nil || !held {
					if err != nil {
						return nil, false, err
					}
					continue
				}
			}
			seen[child] = true
			queue = append(queue, step{gkey: child, path: append(slices.Clone(at.path), t)})
		}
	}
	return nil, false, nil
}
