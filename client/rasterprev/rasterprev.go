// Package rasterprev holds the rendered-text preview cache: one rasterized
// document per (tile, bytes, width), superseded when the tile's bytes or its
// layout width move. Rasterizing is JS-only, so it sits behind the
// Rasterizer interface and tests inject a fake.
package rasterprev

import (
	"math"
	"sort"

	"github.com/josephburnett/gridwell/client/resload"
)

// buckets quantize the layout width so continuous grid zoom re-rasterizes at
// steps, not per frame. They are geometric, each 1/8 wider than the last, so
// the margin a bucket leaves is under a ninth of the box at every width.
var buckets = func() []float64 {
	out := []float64{64}
	for b := 64.0; b < 1<<16; {
		b = math.Floor(b * 9 / 8)
		out = append(out, b)
	}
	return out
}()

// Bucket rounds a logical content width down to the width a raster is made
// at. The raster is drawn at the text's scale, not stretched to the box, so
// rounding down leaves a margin in the tile's own background where rounding
// up would clip the ends of lines.
func Bucket(contentW float64) float64 {
	i := sort.SearchFloat64s(buckets, contentW)
	if i < len(buckets) && buckets[i] == contentW {
		return contentW
	}
	return buckets[max(i-1, 0)]
}

// Key identifies one raster. Bytes is the generation of the body it is made from
// (cache.BodyGen), so new bytes, a plugin's or an edit's, are never taken for
// the old picture's; Org is part of the identity
// because it picks the renderer, and Theme because the document is rasterized
// in the colors on screen.
type Key struct {
	TileID string
	Bytes  uint64
	Bucket float64
	Org    bool
	Theme  string
}

// Raster is the loaded image handle the renderer draws.
type Raster = resload.Resource

// Rasterizer turns an SVG document into a Raster. The wasm rasterizer is
// asynchronous; the test fake resolves on demand.
type Rasterizer interface {
	Rasterize(svg string, onReady func(Raster), onError func())
}

// Cache is not safe for concurrent use, the wasm client being single-threaded.
type Cache struct {
	ras     Rasterizer
	onErr   func(tileID string)
	entries map[slot]*entry
}

// entry is one slot's raster and, while its successor rasterizes, the
// picture it replaces.
type entry struct {
	resload.Entry[Key]
	prev    Raster
	prevKey Key
}

func (e *entry) dropPrev() {
	if e.prev != nil {
		e.prev.Revoke()
	}
	e.prev = nil
}

func (e *entry) release() {
	e.Release()
	e.dropPrev()
}

// onlyBytesDiffer reports that a and b picture other bytes of one document in
// one renderer and theme at one width.
func onlyBytesDiffer(a, b Key) bool {
	a.Bytes = b.Bytes
	return a == b
}

// slot is the map key: a tile at one width bucket. Two consumers at different
// widths would otherwise replace one entry every frame, each revoking the
// other's loading raster.
type slot struct {
	tileID string
	bucket float64
}

// NewCache requires a non-nil ras. onErr fires once per failed key with the
// tile id, so a document that never becomes a picture reaches the user instead
// of silently falling back to raw source; nil silences it.
func NewCache(ras Rasterizer, onErr func(tileID string)) *Cache {
	return &Cache{ras: ras, onErr: onErr, entries: map[slot]*entry{}}
}

// Ensure returns k's raster and the width it was made at, starting a
// rasterization on a miss. While k's is in flight it answers with the nearest
// ready bucket of the same tile, bytes, renderer and theme, so a zoom that
// crosses a bucket keeps showing the document, or else with the picture k's
// slot held before, so an edit does not flash raw source; with neither, and
// once k failed, ok is false and the caller paints raw source. build supplies the SVG and
// reports false when the tile's bytes have not loaded yet, which caches
// nothing. onReady may be nil and fires when a raster lands.
func (c *Cache) Ensure(k Key, build func() (string, bool), onReady func()) (Raster, float64, bool) {
	s := slot{tileID: k.TileID, bucket: k.Bucket}
	if e, ok := c.entries[s]; ok && e.Ident == k {
		if e.Ready() {
			return e.Res, k.Bucket, true
		}
		if e.Failed && e.FailIdent == k {
			return nil, 0, false
		}
		return c.standIn(k)
	}
	svg, ok := build()
	if !ok {
		return c.standIn(k)
	}
	// Other buckets whose bytes moved on re-rasterize on next use; keeping
	// one would draw the previous bytes at the next zoom step.
	for os, old := range c.entries {
		if os != s && os.tileID == k.TileID && old.Ident.Bytes != k.Bytes {
			old.release()
			delete(c.entries, os)
		}
	}
	e, ok := c.entries[s]
	if !ok {
		e = &entry{}
		c.entries[s] = e
	}
	// The slot's last picture of this document stands in until k lands or
	// fails, and no longer.
	switch {
	case e.Ready() && onlyBytesDiffer(e.Ident, k):
		e.dropPrev()
		e.prev, e.prevKey, e.Res = e.Res, e.Ident, nil
	case e.prev != nil && onlyBytesDiffer(e.prevKey, k):
	default:
		e.dropPrev()
	}
	// The slot answers for the key it is rasterizing, so it asks for no
	// second rasterization of the same document.
	e.Adopt(k)
	gen := e.Begin(k)

	c.ras.Rasterize(svg,
		func(r Raster) {
			cur := c.current(s)
			if resload.Take(cur, gen, k, r) {
				c.entries[s].dropPrev()
				if onReady != nil {
					onReady()
				}
			}
		},
		func() {
			if resload.Miss(c.current(s), gen, k) {
				c.entries[s].dropPrev()
				if c.onErr != nil {
					c.onErr(k.TileID)
				}
			}
		},
	)
	return c.standIn(k)
}

// current is slot s's entry, nil once it was dropped.
func (c *Cache) current(s slot) *resload.Entry[Key] {
	if e, ok := c.entries[s]; ok {
		return &e.Entry
	}
	return nil
}

// standIn is the ready raster nearest k's bucket among those identical to k
// in all else, the narrower winning a tie because it cannot clip, or else the
// picture k's own slot held before k.
func (c *Cache) standIn(k Key) (Raster, float64, bool) {
	var best *entry
	for _, e := range c.entries {
		id := e.Ident
		id.Bucket = k.Bucket
		if id != k || !e.Ready() {
			continue
		}
		if best == nil || closer(e.Ident.Bucket, best.Ident.Bucket, k.Bucket) {
			best = e
		}
	}
	if best != nil {
		return best.Res, best.Ident.Bucket, true
	}
	if e, ok := c.entries[slot{tileID: k.TileID, bucket: k.Bucket}]; ok && e.prev != nil && e.Loading(k) {
		return e.prev, e.prevKey.Bucket, true
	}
	return nil, 0, false
}

func closer(a, b, want float64) bool {
	da, db := math.Abs(a-want), math.Abs(b-want)
	return da < db || (da == db && a < b)
}

// Drop releases a tile's rasters. It is idempotent and runs on tile delete.
func (c *Cache) Drop(tileID string) {
	for s, e := range c.entries {
		if s.tileID == tileID {
			e.release()
			delete(c.entries, s)
		}
	}
}

// State is one tile's raster state across its buckets.
type State struct {
	Ready  bool
	Failed bool
}

// States aggregates per tile id, ready when any bucket decoded. The e2e
// testhook is its only reader.
func (c *Cache) States() map[string]State {
	out := map[string]State{}
	for s, e := range c.entries {
		st := out[s.tileID]
		st.Ready = st.Ready || e.Ready()
		st.Failed = st.Failed || e.Failed
		out[s.tileID] = st
	}
	return out
}
