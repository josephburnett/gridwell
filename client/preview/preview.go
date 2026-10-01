// Package preview holds the preview-image cache shared by URL and shell tiles,
// keyed by tile id and remembering which preview_blob_id each image came
// from, so the caller's expected blob id invalidates a stale entry.
package preview

import (
	"math"
	"sync"

	"github.com/josephburnett/gridwell/client/resload"
)

// Image is the decoded handle the renderer draws.
type Image = resload.Resource

// Decoder turns raw JPEG bytes into an Image, possibly asynchronously.
type Decoder interface {
	Decode(bytes []byte, onReady func(Image), onError func())
}

// Cache invalidates an entry when the server-side preview blob id changes.
// Every method is goroutine-safe.
type Cache struct {
	dec      Decoder
	onDecErr func(tileID string)

	mu      sync.Mutex
	entries map[string]*resload.Entry[int64]
}

// localCaptureID keys bytes captured locally before the server's key for them
// was known. Get matches it against any non-zero expected key. It sits below
// every key the node hands out, a plugin picture's negative ones included.
const localCaptureID int64 = math.MinInt64

// NewCache requires a non-nil dec. onDecErr fires once per failed decode with
// the tile id, so bytes that never become a picture reach the user instead of
// vanishing; nil silences it.
func NewCache(dec Decoder, onDecErr func(tileID string)) *Cache {
	return &Cache{
		dec:      dec,
		onDecErr: onDecErr,
		entries:  map[string]*resload.Entry[int64]{},
	}
}

// Get hits when the entry's image is loaded and its blob id matches wantBlobID
// or is a local capture. A wantBlobID of 0 means the server says the tile is
// blank, so an entry keyed to a real blob id misses; a local capture hits,
// being parked ahead of the server.
func (c *Cache) Get(tileID string, wantBlobID int64) (Image, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[tileID]
	if !ok || !e.Ready() {
		return nil, false
	}
	if !holds(e.Ident, wantBlobID) {
		return nil, false
	}
	return e.Res, true
}

// Put stores bytes belonging to a known server-side preview blob. onReady may
// be nil. A newer Put for the same tileID supersedes this one.
func (c *Cache) Put(tileID string, blobID int64, bytes []byte, onReady func()) {
	c.put(tileID, blobID, bytes, onReady)
}

// PutEmpty records that the server answered with no preview, so the fetch does
// not re-fire every draw. An image held for another blob id stays: it is what
// the tile looks like.
func (c *Cache) PutEmpty(tileID string, blobID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at(tileID).Settle(blobID)
}

// Owed reports whether blobID's preview is still to be fetched: not when its
// image is held, decoding, or settled as having none. It is the one question
// a fetch guard asks, so a decode that outlives its fetch is not refetched.
func (c *Cache) Owed(tileID string, blobID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[tileID]
	if !ok {
		return true
	}
	if e.Ready() && holds(e.Ident, blobID) {
		return false
	}
	if e.Loading(blobID) || e.Loading(localCaptureID) {
		return false
	}
	return !(e.Failed && e.FailIdent == blobID)
}

// holds reports whether an image loaded for ident answers wantBlobID.
func holds(ident, wantBlobID int64) bool {
	return ident == localCaptureID || (wantBlobID != 0 && ident == wantBlobID)
}

// PutWildcard serves the flows that hold JPEG bytes before the server blob id
// is known: the URL stream's frames and the shell freeze snapshot. The entry
// matches any non-zero wantBlobID until a specific Put supersedes it.
func (c *Cache) PutWildcard(tileID string, bytes []byte, onReady func()) {
	c.put(tileID, localCaptureID, bytes, onReady)
}

func (c *Cache) put(tileID string, blobID int64, bytes []byte, onReady func()) {
	if len(bytes) == 0 {
		return
	}
	c.mu.Lock()
	// The entry keeps its image and its blob id until the new one lands, so a
	// tile goes on showing the face it had while the next decode runs.
	gen := c.at(tileID).Begin(blobID)
	c.mu.Unlock()

	c.dec.Decode(bytes,
		func(img Image) {
			c.mu.Lock()
			took := resload.Take(c.entries[tileID], gen, blobID, img)
			c.mu.Unlock()
			if took && onReady != nil {
				onReady()
			}
		},
		func() {
			c.mu.Lock()
			// The miss stops the caller re-fetching these bytes every draw.
			missed := resload.Miss(c.entries[tileID], gen, blobID)
			c.mu.Unlock()
			if missed && c.onDecErr != nil {
				c.onDecErr(tileID)
			}
		},
	)
}

// Drop is idempotent and runs on tile delete.
func (c *Cache) Drop(tileID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[tileID]; ok {
		e.Release()
		delete(c.entries, tileID)
	}
}

// at returns tileID's entry, creating it. The caller holds the lock.
func (c *Cache) at(tileID string) *resload.Entry[int64] {
	e, ok := c.entries[tileID]
	if !ok {
		e = &resload.Entry[int64]{}
		c.entries[tileID] = e
	}
	return e
}
