package files

// Find on the files: an id: locates, and an empty query with a filter lists,
// the filters the node's, applied to the rows it holds (store.tileFilter). A
// text query answers empty: no body is read for it.

import (
	"context"
	"slices"
	"strings"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
)

// findHit is one placed hit and the row behind it, 0 for a file with none.
type findHit struct {
	result *pb.FindResult
	row    int64
}

func (s *Source) Find(_ context.Context, req *pb.FindRequest) (*pb.FindResponse, error) {
	g := &findGrids{s: s, byDir: map[string]*listing{}}
	if q := rpc.ParseFindQuery(req.Query); q.ID != "" {
		return s.locate(req, g, q.ID)
	}
	if strings.TrimSpace(req.Query) != "" || !rpc.FindFiltered(req) {
		return &pb.FindResponse{}, nil
	}
	var hits []findHit
	var err error
	if req.GridId != "" {
		dir, derr := s.dirOf(req.GridId)
		if derr != nil {
			return nil, derr
		}
		// A directory no held way reaches lists with paths that start at it.
		var above []*pb.Tile
		if above, _, err = g.chain(dir); err == nil {
			hits, err = g.walk(dir, above, req.Recurse, map[string]bool{})
		}
	} else {
		hits, err = g.rows(req)
	}
	if err != nil {
		return nil, err
	}
	return s.filterHits(req, hits, true)
}

// locate is the id: mode, home's rule: the one tile, when it passes the
// filters, on its chain of doors, and nothing for an id naming no file.
func (s *Source) locate(req *pb.FindRequest, g *findGrids, id string) (*pb.FindResponse, error) {
	path, dir, err := s.tilePath(id)
	switch {
	case gwerr.IsDeadLink(err), rpc.ShapeOf(id) != rpc.ShapeKey:
		return &pb.FindResponse{}, nil
	case err != nil:
		return nil, err
	}
	above, ok, err := g.chain(dir)
	if err != nil || !ok {
		return &pb.FindResponse{}, err
	}
	h, err := g.hit(dir, path, above)
	if err != nil || h == nil {
		return &pb.FindResponse{}, err
	}
	return s.filterHits(req, []findHit{*h}, false)
}

// filterHits keeps the hits whose rows pass the filters, and the kind a hit
// with no row answers with; a listing is newest first and capped, as home's
// is.
func (s *Source) filterHits(req *pb.FindRequest, hits []findHit, listing bool) (*pb.FindResponse, error) {
	out := &pb.FindResponse{}
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
	passing, err := s.mem.FindPassing(ids, req)
	if err != nil {
		return nil, err
	}
	kept := hits[:0]
	for _, h := range hits {
		if _, ok := passing[h.row]; ok || (h.row == 0 && !rpc.FindNeedsRow(req)) {
			kept = append(kept, h)
		}
	}
	if listing {
		// A file with no row has no time and lists after every row.
		slices.SortStableFunc(kept, func(x, y findHit) int { return int(passing[y.row] - passing[x.row]) })
		kept = kept[:min(len(kept), rpc.FindLimit(req))]
	}
	for _, h := range kept {
		out.Results = append(out.Results, h.result)
	}
	return out, nil
}

// findGrids lists each directory a find reads once.
type findGrids struct {
	s     *Source
	byDir map[string]*listing
}

func (g *findGrids) grid(dir string) (*listing, error) {
	if l, ok := g.byDir[dir]; ok {
		return l, nil
	}
	l, err := g.s.synthesize(dir)
	if err != nil {
		return nil, err
	}
	g.byDir[dir] = l
	return l, nil
}

// hit is path's tile in dir, nil when dir lists none.
func (g *findGrids) hit(dir, path string, above []*pb.Tile) (*findHit, error) {
	l, err := g.grid(dir)
	if err != nil {
		return nil, err
	}
	if i := l.at(path); i >= 0 {
		return &findHit{row: l.rows[i].ID, result: &pb.FindResult{Tile: l.tiles[i], Path: above, Score: 1}}, nil
	}
	return nil, nil
}

// childDir is the directory a door tile opens, "" for any other tile.
func childDir(t *pb.Tile) string {
	if t.ChildGridId == "" {
		return ""
	}
	dir, err := pathOf(t.ChildGridId)
	if err != nil {
		return ""
	}
	return dir
}

// walk lists dir and, with recurse, every directory its doors open, each
// tile placed under the doors that reached it.
func (g *findGrids) walk(dir string, above []*pb.Tile, recurse bool, seen map[string]bool) ([]findHit, error) {
	seen[dir] = true
	l, err := g.grid(dir)
	if err != nil {
		return nil, err
	}
	var out []findHit
	for i, t := range l.tiles {
		out = append(out, findHit{row: l.rows[i].ID, result: &pb.FindResult{Tile: t, Path: above, Score: 1}})
	}
	if !recurse {
		return out, nil
	}
	for _, t := range l.tiles {
		child := childDir(t)
		if child == "" || seen[child] {
			continue
		}
		under, err := g.walk(child, append(slices.Clone(above), t), true, seen)
		if gwerr.IsDeadLink(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, under...)
	}
	return out, nil
}

// rows is an empty query with no grid: the rows whose facts pass, each placed
// on its chain, and dropped without one.
func (g *findGrids) rows(req *pb.FindRequest) ([]findHit, error) {
	found, err := g.s.mem.FindRows(req, rpc.FindLimit(req))
	if err != nil {
		return nil, err
	}
	var out []findHit
	for _, r := range found {
		h, err := g.placed(r)
		if err != nil {
			return nil, err
		}
		if h != nil {
			out = append(out, *h)
		}
	}
	return out, nil
}

func (g *findGrids) placed(r store.FindRow) (*findHit, error) {
	dir, err := g.s.mem.GridKey(r.GridID)
	if err != nil {
		return nil, err
	}
	above, ok, err := g.chain(dir)
	if err != nil || !ok {
		return nil, err
	}
	return g.hit(dir, r.Key, above)
}

// chain is the doors from a root down to dir, found by listing down from each
// root through the directories the node holds rows for, the ones the user
// has touched: a find never walks the disk. ok is false when no such way
// down reaches it.
func (g *findGrids) chain(target string) (above []*pb.Tile, ok bool, err error) {
	type step struct {
		dir   string
		above []*pb.Tile
	}
	var queue []step
	seen := map[string]bool{}
	for _, r := range g.s.roots {
		if !seen[r.Path] {
			seen[r.Path] = true
			queue = append(queue, step{dir: r.Path})
		}
	}
	for len(queue) > 0 {
		at := queue[0]
		queue = queue[1:]
		if at.dir == target {
			return at.above, true, nil
		}
		l, err := g.grid(at.dir)
		if gwerr.IsDeadLink(err) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		for _, t := range l.tiles {
			child := childDir(t)
			if child == "" || seen[child] {
				continue
			}
			if child != target {
				if _, held, err := g.s.mem.LookupGrid(child); err != nil || !held {
					if err != nil {
						return nil, false, err
					}
					continue
				}
			}
			seen[child] = true
			queue = append(queue, step{dir: child, above: append(slices.Clone(at.above), t)})
		}
	}
	return nil, false, nil
}
