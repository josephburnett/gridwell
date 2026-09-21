// Package preview holds the preview-image cache shared by URL and shell tiles.
// It keys a decoded Image by tile id and remembers which preview_blob_id it
// came from, so Get taking the caller's expected blob id invalidates a stale
// entry with no explicit invalidation signal. Decoding is JS-only, so it sits
// behind the Decoder interface and tests inject a synchronous fake.
package preview

import (
	"sync"

	"github.com/josephburnett/gridwell/client/resload"
)

// Image is the decoded handle the renderer draws.
type Image = resload.Resource

// Decoder turns raw JPEG bytes into an Image. The wasm decoder is
// asynchronous; the test fake resolves inside Decode so tests are
// deterministic.
type Decoder interface {
	Decode(bytes []byte, onReady func(Image), onError func())
}

// Cache invalidates an entry when the server-side preview blob id changes. One
// mutex protects the entry map, so every method is goroutine-safe.
type Cache struct {
	dec      Decoder
	onDecErr func(tileID string)

	mu      sync.Mutex
	entries map[string]*resload.Entry[int64]
}

// ungeneratedBlobID keys a face the server minted no generation for: a page's,
// and bytes captured locally before the server blob id was known. Get matches
// it against any non-zero expected blob id.
const ungeneratedBlobID int64 = -1

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
// or is ungenerated. A wantBlobID of 0 means the server says the tile is
// blank, so an entry keyed to a real blob id misses; an ungenerated entry
// hits, being a local capture parked ahead of the server.
func (c *Cache) Get(tileID string, wantBlobID int64) (Image, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[tileID]
	if !ok || !e.Ready() {
		return nil, false
	}
	if e.Ident != ungeneratedBlobID && (wantBlobID == 0 || e.Ident != wantBlobID) {
		return nil, false
	}
	return e.Res, true
}

// Put stores bytes belonging to a known server-side preview blob; locally
// captured bytes go through PutWildcard. onReady may be nil and fires once the
// entry is installed. A newer Put for the same tileID supersedes this one,
// discarding the late result silently.
func (c *Cache) Put(tileID string, blobID int64, bytes []byte, onReady func()) {
	c.put(tileID, blobID, bytes, onReady)
}

// PutEmpty records that the server answered with no preview. A completed fetch
// settles the cache either way, an unsettled empty result re-firing on every
// draw. A later Put, or a changed blob id, supersedes it. An image already
// held for another blob id stays: it is what the tile looks like.
func (c *Cache) PutEmpty(tileID string, blobID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at(tileID).Settle(blobID)
}

// KnownEmpty answers whether a completed fetch or decode already settled this
// blob id as having no image, which is how a caller stops re-fetching.
func (c *Cache) KnownEmpty(tileID string, blobID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[tileID]
	return ok && e.Failed && e.FailIdent == blobID
}

// PutWildcard serves the flows that hold JPEG bytes before the server blob id
// is known: the URL stream's frames and the shell freeze snapshot. The entry
// matches any non-zero wantBlobID until a specific Put supersedes it.
func (c *Cache) PutWildcard(tileID string, bytes []byte, onReady func()) {
	c.put(tileID, ungeneratedBlobID, bytes, onReady)
}

func (c *Cache) put(tileID string, blobID int64, bytes []byte, onReady func()) {
	if len(bytes) == 0 {
		return
	}
	c.mu.Lock()
	// The entry keeps its image and its blob id until the new one lands, so a
	// tile goes on showing the face it had while the next decode runs.
	gen := c.at(tileID).Begin()
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
			// No image is installed, so Get keeps returning the prior image if
			// there is one; the miss is what stops the caller re-fetching
			// these bytes on every draw.
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
