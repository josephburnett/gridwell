// Package rasterprev holds the rendered-text preview cache: one rasterized
// document per (tile, version, width bucket), superseded when the tile's bytes
// or its layout width move. Rasterizing is JS-only, so it sits behind the
// Rasterizer interface and tests inject a fake.
package rasterprev

import (
	"math"

	"github.com/josephburnett/gridwell/client/resload"
)

// bucketPx quantizes the layout width so continuous grid zoom re-rasterizes at
// steps, not per frame.
const bucketPx = 64.0

// Bucket rounds a logical content width to the width a raster is made at.
func Bucket(contentW float64) float64 {
	return math.Max(bucketPx, math.Round(contentW/bucketPx)*bucketPx)
}

// Key identifies one raster. Version is the tile's, so new bytes never draw
// the old picture; Org is part of the identity because it picks the renderer,
// and Theme because the document is rasterized in the colors on screen.
type Key struct {
	TileID  string
	Version int64
	Bucket  float64
	Org     bool
	Theme   string
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
	entries map[slot]*resload.Entry[Key]
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
	return &Cache{ras: ras, onErr: onErr, entries: map[slot]*resload.Entry[Key]{}}
}

// Ensure returns k's raster, starting a rasterization on a miss. ok is false
// while one is in flight and stays false once one failed, so the caller paints
// raw source. build supplies the SVG and reports false when the tile's bytes
// have not loaded yet, which caches nothing. onReady may be nil and fires when
// a raster lands.
func (c *Cache) Ensure(k Key, build func() (string, bool), onReady func()) (Raster, bool) {
	s := slot{tileID: k.TileID, bucket: k.Bucket}
	if e, ok := c.entries[s]; ok && e.Ident == k {
		if e.Ready() {
			return e.Res, true
		}
		return nil, false
	}
	svg, ok := build()
	if !ok {
		return nil, false
	}
	// Other buckets whose version moved on re-rasterize on next use; keeping
	// one would draw the previous bytes at the next zoom step.
	for os, old := range c.entries {
		if os != s && os.tileID == k.TileID && old.Ident.Version != k.Version {
			old.Release()
			delete(c.entries, os)
		}
	}
	e, ok := c.entries[s]
	if !ok {
		e = &resload.Entry[Key]{}
		c.entries[s] = e
	}
	// The slot answers for the key it is rasterizing, so a frame mid-flight
	// paints raw source instead of the previous version's picture, and asks
	// for no second rasterization of the same document.
	e.Adopt(k)
	gen := e.Begin()

	c.ras.Rasterize(svg,
		func(r Raster) {
			if resload.Take(c.entries[s], gen, k, r) && onReady != nil {
				onReady()
			}
		},
		func() {
			if resload.Miss(c.entries[s], gen, k) && c.onErr != nil {
				c.onErr(k.TileID)
			}
		},
	)
	return nil, false
}

// Drop releases a tile's rasters. It is idempotent and runs on tile delete.
func (c *Cache) Drop(tileID string) {
	for s, e := range c.entries {
		if s.tileID == tileID {
			e.Release()
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
