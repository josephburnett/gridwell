// Package preview holds the preview-image cache shared by URL tiles
// and shell tiles. Both tile kinds store a JPEG in the server's blobs
// table whose id is the tile's preview_blob_id; the cache turns those
// bytes into a decoded Image keyed by tile id and tracks which blob
// id each cached entry was loaded from. Get takes the caller's
// expected blob id, so a server-side preview change (a shell freezing
// a new frame, a URL stream landing a new blob) automatically
// invalidates the stale entry on the next Get — no explicit
// invalidation signal required.
//
// The decode step itself is JS-only (Blob + createObjectURL + new
// Image() in the browser), so it lives behind a Decoder interface.
// The wasm build ships a real decoder; unit tests inject a fake that
// resolves synchronously.
package preview

import "sync"

// Image is the decoded handle the renderer draws. Truthy reports
// whether the underlying browser resource is loaded; Revoke releases
// the backing object URL. In the wasm build this wraps an
// HTMLImageElement plus its createObjectURL. In tests this is a
// minimal struct that records its revoked state.
type Image interface {
	Truthy() bool
	Revoke()
}

// Decoder turns raw JPEG bytes into an Image. onReady fires with the
// decoded image on success; onError fires on failure. The wasm
// decoder is asynchronous (browser image decode); the test fake
// resolves synchronously inside Decode for determinism.
type Decoder interface {
	Decode(bytes []byte, onReady func(Image), onError func())
}

// Cache is a tile-id-keyed image cache that auto-invalidates when
// the server-side preview blob id changes. Concurrency: every method
// is safe to call from multiple goroutines; internally a single
// mutex protects the entry map.
type Cache struct {
	dec Decoder

	mu       sync.Mutex
	entries  map[string]*entry
	fetching map[string]bool
}

// entry holds one tile's cached preview state.
//
//	blobID  — the preview_blob_id this image was decoded from.
//	          wildcardBlobID for locally-captured bytes (live URL
//	          stream frame, shell freeze snapshot) whose final
//	          server blob id isn't yet known.
//	image   — the decoded handle, or nil while decoding is pending.
//	gen     — monotonic counter. Each Put bumps it; if a decode's
//	          onReady fires after a newer Put has superseded it, the
//	          stale result is discarded.
type entry struct {
	blobID int64
	image  Image
	gen    int64
	// empty records a COMPLETED fetch that answered "no preview" for
	// blobID — a settled miss, not an unanswered one. Without it every
	// frame re-asks the server for tiles that will never have a preview
	// (one RPC per non-decodable tile per draw, forever — #265).
	empty bool
}

// wildcardBlobID marks an entry whose bytes were captured locally
// before the server-side blob id was known. Get treats it as a match
// for any non-zero expected blob id.
const wildcardBlobID int64 = -1

// NewCache returns a Cache backed by the given Decoder. The Decoder
// must be non-nil.
func NewCache(dec Decoder) *Cache {
	return &Cache{
		dec:      dec,
		entries:  map[string]*entry{},
		fetching: map[string]bool{},
	}
}

// Get returns the cached image for tileID iff:
//
//   - an entry exists,
//   - the entry's image is loaded (Truthy),
//   - and the entry's recorded blob id matches wantBlobID (or is
//     wildcard).
//
// wantBlobID == 0 means the tile has no server-side preview yet. An entry
// keyed to a REAL blob id misses in that case (it is server state that may
// be stale — don't show a cached image on a tile the server says is blank),
// but a WILDCARD entry hits: it is a local capture parked ahead of the
// server (the first-ever freeze of a url/shell tile, whose PreviewBlobID is
// still 0 until the SetURLState/SetShellPreview echo lands), by definition
// fresher than anything the server knows.
func (c *Cache) Get(tileID string, wantBlobID int64) (Image, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[tileID]
	if !ok || e.image == nil || !e.image.Truthy() {
		return nil, false
	}
	if e.blobID != wildcardBlobID && (wantBlobID == 0 || e.blobID != wantBlobID) {
		return nil, false
	}
	return e.image, true
}

// Put decodes bytes and stores them under (tileID, blobID). Use
// when the bytes correspond to a known server-side preview blob —
// e.g. the result of GetTilePreview. For locally-captured bytes
// whose final blob id isn't yet known, use PutWildcard.
//
// onReady fires (with no arguments) once decode completes and the
// entry is installed; it may be nil. If a newer Put for the same
// tileID supersedes this one before decode finishes, the late
// result is discarded and onReady is not called.
func (c *Cache) Put(tileID string, blobID int64, bytes []byte, onReady func()) {
	c.put(tileID, blobID, bytes, onReady)
}

// PutEmpty records that the server ANSWERED with no preview for
// (tileID, blobID). A completed fetch must settle the cache either way;
// an unsettled empty result re-fires the fetch on every draw. A later
// Put with real bytes, or a changed blob id, supersedes it.
func (c *Cache) PutEmpty(tileID string, blobID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[tileID]; ok && e.image != nil && e.image.Truthy() {
		return // never downgrade a real image to a recorded miss
	}
	c.entries[tileID] = &entry{blobID: blobID, empty: true}
}

// KnownEmpty reports a recorded "no preview" answer for (tileID, blobID)
// — the caller skips the fetch instead of re-asking every frame.
func (c *Cache) KnownEmpty(tileID string, blobID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[tileID]
	return ok && e.empty && e.blobID == blobID
}

// PutWildcard decodes bytes and stores them under tileID with the
// wildcard sentinel. Used by flows that have the JPEG bytes in hand
// before the server-side blob id is known: the URL stream's
// frame-by-frame WebSocket payload and the shell freeze snapshot.
// The entry matches any non-zero wantBlobID in Get until a specific
// Put supersedes it.
func (c *Cache) PutWildcard(tileID string, bytes []byte, onReady func()) {
	c.put(tileID, wildcardBlobID, bytes, onReady)
}

func (c *Cache) put(tileID string, blobID int64, bytes []byte, onReady func()) {
	if len(bytes) == 0 {
		return
	}
	c.mu.Lock()
	e, ok := c.entries[tileID]
	if !ok {
		e = &entry{}
		c.entries[tileID] = e
	}
	e.gen++
	gen := e.gen
	c.mu.Unlock()

	c.dec.Decode(bytes,
		func(img Image) {
			c.mu.Lock()
			cur, ok := c.entries[tileID]
			if !ok || cur.gen != gen {
				// Superseded or dropped before decode completed.
				c.mu.Unlock()
				if img != nil {
					img.Revoke()
				}
				return
			}
			if cur.image != nil && cur.image.Truthy() {
				cur.image.Revoke()
			}
			cur.image = img
			cur.blobID = blobID
			c.mu.Unlock()
			if onReady != nil {
				onReady()
			}
		},
		func() {
			// Decode failed. The entry's gen has already been bumped
			// (so any in-flight predecessor for the same tile will
			// also discard), but no image is installed. Subsequent
			// Get will continue to return the prior image if one
			// exists, or not-ok if not.
		},
	)
}

// Drop removes the entry for tileID and revokes its image, if any.
// Idempotent. Called when a tile is deleted.
func (c *Cache) Drop(tileID string) {
	c.mu.Lock()
	e, ok := c.entries[tileID]
	if ok {
		delete(c.entries, tileID)
	}
	c.mu.Unlock()
	if ok && e.image != nil && e.image.Truthy() {
		e.image.Revoke()
	}
}

// MarkFetching atomically claims an in-flight fetch slot for tileID.
// Returns false if a fetch was already in flight — callers must skip
// the duplicate request. Pair with ClearFetching once the fetch
// finishes (success or failure).
func (c *Cache) MarkFetching(tileID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fetching[tileID] {
		return false
	}
	c.fetching[tileID] = true
	return true
}

// ClearFetching releases the in-flight slot for tileID. Idempotent.
func (c *Cache) ClearFetching(tileID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.fetching, tileID)
}
