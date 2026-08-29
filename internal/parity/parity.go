// Package parity is the v2 program's oracle (docs/v2-design.md §8.4): it
// crawls a Gridwell node over the wire — the same surface every client
// sees — into a comparable Snapshot, and diffs two Snapshots under an
// explicit Policy. The migration gate is "zero differences between the
// old binary and the converted home"; the differ therefore treats every
// field as significant unless the policy names it, so a NEW fact shows
// up as a difference until a human decides it is legitimate (the
// refuse-the-unknown rule applied to verification).
//
// Two crawl shapes:
//   - Crawl: one node → one Snapshot (also proves "reading never
//     mutates": two crawls of the same node must diff empty).
//   - CrawlPair: two nodes serving the SAME logical data (old vs
//     converted) walked grid-by-grid back to back, so a racy source
//     (the live filesystem under an fs projection) is read by both
//     sides milliseconds apart instead of a whole crawl apart.
package parity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/rpc"
)

// Options bounds a crawl. The zero value is the conservative default:
// stop at transit boundaries, fetch content and previews, no grid cap.
type Options struct {
	// IncludeTransit descends into chained grids (ids deeper than
	// <namespace>/<grid>). Default false: a remote node's data is
	// compared by crawling that node directly — its layout is its own.
	IncludeTransit bool
	// SkipContent / SkipPreviews drop the byte-level fetches (content
	// sha256, preview sha256) for a faster structural pass.
	SkipContent  bool
	SkipPreviews bool
	// MaxGrids aborts the crawl (with an error, never a silent cap)
	// when more than this many grids have been visited. 0 = unlimited.
	MaxGrids int
	// GridAllow, when non-nil, bounds descent to these qualified grid
	// ids — the converter-scoped mode: compare exactly the grids the old
	// DB had materialized, without materializing the rest of a live
	// source on both sides. Skipped grids are recorded, never silent.
	GridAllow map[string]bool
	// NSAllow, when non-nil, allows EVERY grid of the named namespaces
	// (first id segment) regardless of GridAllow — the whole-home gate's
	// hybrid: fully crawl the finite stores (local, the launcher),
	// scope-crawl the unbounded projections (fs).
	NSAllow map[string]bool
}

// allowed reports whether a grid id is inside the crawl scope.
func (o Options) allowed(gid string) bool {
	if o.GridAllow == nil && o.NSAllow == nil {
		return true
	}
	if o.NSAllow != nil {
		ns, _, _ := strings.Cut(gid, "/")
		if o.NSAllow[ns] {
			return true
		}
	}
	return o.GridAllow[gid]
}

// TooDeep reports whether a qualified grid id crosses a transit
// boundary: more than one separator means <ns>/<sub-ns>/... — a chain
// served by another node, whose layout that node owns.
func TooDeep(gridID string) bool {
	return strings.Count(gridID, "/") > 1
}

// GridRecord is one grid as crawled: its own normalized fields, the
// sorted ids of its tiles, or the error code the read answered with.
type GridRecord struct {
	Fields  map[string]any `json:"fields,omitempty"`
	TileIDs []string       `json:"tile_ids,omitempty"`
	Err     string         `json:"err,omitempty"`
}

// ContentRecord summarizes one tile's content stream.
type ContentRecord struct {
	SHA256    string `json:"sha256"`
	Bytes     int    `json:"bytes"`
	MediaType string `json:"media_type"`
	Version   int64  `json:"version"`
	Err       string `json:"err,omitempty"`
}

// Snapshot is one node's crawled state, keyed by qualified ids.
type Snapshot struct {
	Plugins  map[string]map[string]any `json:"plugins"` // by uuid
	Grids    map[string]GridRecord     `json:"grids"`
	Tiles    map[string]map[string]any `json:"tiles"`
	Contents map[string]ContentRecord  `json:"contents,omitempty"`
	Previews map[string]string         `json:"previews,omitempty"` // sha256, "" = no preview
	// Skipped lists grid ids seen but not descended (transit bounds) —
	// visible, never silent.
	Skipped []string `json:"skipped,omitempty"`
}

// normalize renders any JSON-taggable value to a flat field map so the
// differ compares by field NAME (json tag) — new wire fields surface
// automatically instead of being silently equal.
func normalize(v any) map[string]any {
	raw, err := json.Marshal(v)
	if err != nil {
		return map[string]any{"_marshal_error": err.Error()}
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]any{"_unmarshal_error": err.Error()}
	}
	// Drop empty-value noise so omitempty vs explicit-zero never diffs.
	for k, val := range m {
		switch t := val.(type) {
		case string:
			if t == "" {
				delete(m, k)
			}
		case float64:
			if t == 0 {
				delete(m, k)
			}
		case bool:
			if !t {
				delete(m, k)
			}
		case nil:
			delete(m, k)
		}
	}
	return m
}

// errCode reduces an RPC failure to its Connect code: comparable across
// binaries, while message text (which may legitimately evolve) is not.
func errCode(err error) string {
	if err == nil {
		return ""
	}
	var cerr *connect.Error
	if errors.As(err, &cerr) {
		return cerr.Code().String()
	}
	return "error"
}

// CrawlPair walks two nodes that claim to serve the same logical data.
// The BFS frontier is the UNION of both sides' reachable grids, and each
// grid is fetched from both sides back to back, minimizing the window in
// which a live source (disk, /proc) can change between the two reads.
func CrawlPair(ctx context.Context, a, b *rpc.Client, o Options) (*Snapshot, *Snapshot, error) {
	snaps, err := crawl(ctx, []*rpc.Client{a, b}, o)
	if err != nil {
		return nil, nil, err
	}
	return snaps[0], snaps[1], nil
}

func crawl(ctx context.Context, cls []*rpc.Client, o Options) ([]*Snapshot, error) {
	snaps := make([]*Snapshot, len(cls))
	for i := range snaps {
		snaps[i] = &Snapshot{
			Plugins:  map[string]map[string]any{},
			Grids:    map[string]GridRecord{},
			Tiles:    map[string]map[string]any{},
			Contents: map[string]ContentRecord{},
			Previews: map[string]string{},
		}
	}

	// Roots: the node grid plus every plugin's declared grids.
	var queue []string
	seen := map[string]bool{}
	enqueue := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		queue = append(queue, id)
	}
	for i, cl := range cls {
		pl, err := cl.ListPlugins(ctx)
		if err != nil {
			return nil, fmt.Errorf("parity: ListPlugins side %d: %w", i, err)
		}
		for _, p := range pl.Plugins {
			snaps[i].Plugins[p.UUID] = normalize(p)
			enqueue(p.RootGridID)
			enqueue(p.InstanceGridID)
			enqueue(p.ScratchGridID)
		}
	}

	visited := 0
	for len(queue) > 0 {
		gid := queue[0]
		queue = queue[1:]
		if (TooDeep(gid) && !o.IncludeTransit) || !o.allowed(gid) {
			for _, s := range snaps {
				s.Skipped = append(s.Skipped, gid)
			}
			continue
		}
		visited++
		if o.MaxGrids > 0 && visited > o.MaxGrids {
			return nil, fmt.Errorf("parity: crawl exceeded MaxGrids=%d — raise the bound or scope the crawl; a silent cap would read as full coverage", o.MaxGrids)
		}
		for i, cl := range cls {
			s := snaps[i]
			resp, err := cl.GetGrid(ctx, gid)
			if err != nil {
				s.Grids[gid] = GridRecord{Err: errCode(err)}
				continue
			}
			rec := GridRecord{Fields: normalize(resp.Grid)}
			for _, t := range resp.Tiles {
				t := t
				rec.TileIDs = append(rec.TileIDs, t.ID)
				s.Tiles[t.ID] = normalize(&t)
				enqueue(t.ChildGridID)
				if !o.SkipContent && t.Kind == rpc.KindText {
					data, mt, ver, cerr := cl.ReadContent(ctx, t.ID)
					cr := ContentRecord{Err: errCode(cerr)}
					if cerr == nil {
						sum := sha256.Sum256(data)
						cr = ContentRecord{SHA256: hex.EncodeToString(sum[:]), Bytes: len(data), MediaType: mt, Version: ver}
					}
					s.Contents[t.ID] = cr
				}
				if !o.SkipPreviews && (t.Kind == rpc.KindURL || t.Kind == rpc.KindShell) {
					jpeg, perr := cl.GetTilePreview(ctx, t.ID)
					switch {
					case perr != nil:
						s.Previews[t.ID] = "err:" + errCode(perr)
					case len(jpeg) == 0:
						s.Previews[t.ID] = ""
					default:
						sum := sha256.Sum256(jpeg)
						s.Previews[t.ID] = hex.EncodeToString(sum[:])
					}
				}
			}
			sort.Strings(rec.TileIDs)
			s.Grids[gid] = rec
		}
	}
	for _, s := range snaps {
		sort.Strings(s.Skipped)
	}
	return snaps, nil
}

// Policy names every deliberate blind spot of a diff. An empty Policy
// compares everything.
type Policy struct {
	// IgnoreFields skips these normalized field names (json tags) on
	// tiles, grids, and plugin records — e.g. "status_detail" (live
	// trouble), "stale" (cache stamping), "info_error".
	IgnoreFields map[string]bool
	// VolatileNS names namespaces (first id segment) whose ENTRY SETS
	// churn between reads (proc). There, presence differences and
	// content differences are suppressed; tiles present on both sides
	// are still compared field-for-field (remembered placement must
	// match).
	VolatileNS map[string]bool
	// AllowIDs, when non-nil, restricts tile comparison to these
	// qualified ids — the converter emits the pre-existing id set so
	// rows minted DURING the crawl (a directory listed for the first
	// time on both sides) don't need identical fresh ids to pass.
	AllowIDs map[string]bool
}

func (p Policy) volatile(id string) bool {
	if p.VolatileNS == nil {
		return false
	}
	ns, _, _ := strings.Cut(id, "/")
	return p.VolatileNS[ns]
}

func (p Policy) allowed(id string) bool { return p.AllowIDs == nil || p.AllowIDs[id] }

// Diff compares two Snapshots under a Policy. Empty result = parity.
// Each difference is one human-readable line; the slice is sorted.
func Diff(a, b *Snapshot, p Policy) []string {
	var out []string
	add := func(format string, args ...any) { out = append(out, fmt.Sprintf(format, args...)) }

	diffMaps := func(kind, id string, ma, mb map[string]any) {
		keys := map[string]bool{}
		for k := range ma {
			keys[k] = true
		}
		for k := range mb {
			keys[k] = true
		}
		for k := range keys {
			if p.IgnoreFields[k] {
				continue
			}
			va, vb := ma[k], mb[k]
			if fmt.Sprintf("%v", va) != fmt.Sprintf("%v", vb) {
				add("%s %s: %s: %v != %v", kind, id, k, va, vb)
			}
		}
	}

	// Plugins.
	for uuid, ma := range a.Plugins {
		if mb, ok := b.Plugins[uuid]; ok {
			diffMaps("plugin", uuid, ma, mb)
		} else {
			add("plugin %s: only in A", uuid)
		}
	}
	for uuid := range b.Plugins {
		if _, ok := a.Plugins[uuid]; !ok {
			add("plugin %s: only in B", uuid)
		}
	}

	// Grids.
	for gid, ga := range a.Grids {
		gb, ok := b.Grids[gid]
		if !ok {
			add("grid %s: only in A", gid)
			continue
		}
		if ga.Err != gb.Err {
			add("grid %s: err: %q != %q", gid, ga.Err, gb.Err)
			continue
		}
		diffMaps("grid", gid, ga.Fields, gb.Fields)
		if !p.volatile(gid) {
			ta := filterIDs(ga.TileIDs, p)
			tb := filterIDs(gb.TileIDs, p)
			if strings.Join(ta, ",") != strings.Join(tb, ",") {
				add("grid %s: tiles: [%s] != [%s]", gid, strings.Join(ta, " "), strings.Join(tb, " "))
			}
		}
	}
	for gid := range b.Grids {
		if _, ok := a.Grids[gid]; !ok {
			add("grid %s: only in B", gid)
		}
	}

	// Tiles (union of both sides, minus volatile-presence noise).
	for tid, ma := range a.Tiles {
		if !p.allowed(tid) {
			continue
		}
		mb, ok := b.Tiles[tid]
		if !ok {
			if !p.volatile(tid) {
				add("tile %s: only in A", tid)
			}
			continue
		}
		diffMaps("tile", tid, ma, mb)
	}
	for tid := range b.Tiles {
		if !p.allowed(tid) {
			continue
		}
		if _, ok := a.Tiles[tid]; !ok && !p.volatile(tid) {
			add("tile %s: only in B", tid)
		}
	}

	// Contents and previews.
	for tid, ca := range a.Contents {
		if !p.allowed(tid) || p.volatile(tid) {
			continue
		}
		cb, ok := b.Contents[tid]
		if !ok {
			add("content %s: only in A", tid)
			continue
		}
		if ca != cb {
			add("content %s: %+v != %+v", tid, ca, cb)
		}
	}
	for tid := range b.Contents {
		if !p.allowed(tid) || p.volatile(tid) {
			continue
		}
		if _, ok := a.Contents[tid]; !ok {
			add("content %s: only in B", tid)
		}
	}
	for tid, pa := range a.Previews {
		if !p.allowed(tid) || p.volatile(tid) {
			continue
		}
		if pb, ok := b.Previews[tid]; ok {
			if pa != pb {
				add("preview %s: %s != %s", tid, pa, pb)
			}
		} else {
			add("preview %s: only in A", tid)
		}
	}
	for tid := range b.Previews {
		if !p.allowed(tid) || p.volatile(tid) {
			continue
		}
		if _, ok := a.Previews[tid]; !ok {
			add("preview %s: only in B", tid)
		}
	}

	sort.Strings(out)
	return out
}

func filterIDs(ids []string, p Policy) []string {
	if p.AllowIDs == nil {
		return ids
	}
	var out []string
	for _, id := range ids {
		if p.AllowIDs[id] {
			out = append(out, id)
		}
	}
	return out
}
