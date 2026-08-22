package preview

import (
	"sync"
	"sync/atomic"
	"testing"
)

// fakeImage records bytes + revoked state so tests can assert
// resource-release semantics.
type fakeImage struct {
	bytes   []byte
	revoked atomic.Bool
}

func (i *fakeImage) Truthy() bool { return !i.revoked.Load() }
func (i *fakeImage) Revoke()      { i.revoked.Store(true) }

// fakeDecoder buffers Decode calls so tests can resolve them in any
// order. This is the difference between "easy to test" and "can test
// the concurrent-Put invariant": a synchronous decoder would always
// resolve in call order, hiding races.
type fakeDecoder struct {
	mu      sync.Mutex
	pending []pendingDecode
}

type pendingDecode struct {
	bytes   []byte
	onReady func(Image)
	onError func()
}

func (d *fakeDecoder) Decode(b []byte, onReady func(Image), onError func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pending = append(d.pending, pendingDecode{append([]byte(nil), b...), onReady, onError})
}

// resolveAll fires onReady for every queued decode, in order, with a
// fakeImage carrying the corresponding bytes. Returns the resulting
// images so tests can inspect Revoke() calls on them.
func (d *fakeDecoder) resolveAll() []*fakeImage {
	d.mu.Lock()
	pending := d.pending
	d.pending = nil
	d.mu.Unlock()
	out := make([]*fakeImage, len(pending))
	for i, p := range pending {
		img := &fakeImage{bytes: p.bytes}
		out[i] = img
		p.onReady(img)
	}
	return out
}

// resolve fires the i-th queued decode in isolation, leaving later
// decodes still pending. Used to assert ordering invariants.
func (d *fakeDecoder) resolve(i int) *fakeImage {
	d.mu.Lock()
	p := d.pending[i]
	d.pending = append(d.pending[:i], d.pending[i+1:]...)
	d.mu.Unlock()
	img := &fakeImage{bytes: p.bytes}
	p.onReady(img)
	return img
}

// failNext fires onError for the i-th queued decode.
func (d *fakeDecoder) failNext(i int) {
	d.mu.Lock()
	p := d.pending[i]
	d.pending = append(d.pending[:i], d.pending[i+1:]...)
	d.mu.Unlock()
	p.onError()
}

func (d *fakeDecoder) pendingCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.pending)
}

// TestGetEmptyReturnsNotOK: a brand-new cache has nothing in it.
// Establishes baseline before any Put-driven test runs against a
// dirty cache by accident.
func TestGetEmptyReturnsNotOK(t *testing.T) {
	c := NewCache(&fakeDecoder{})
	if _, ok := c.Get("42", 1); ok {
		t.Errorf("Get on empty cache returned ok")
	}
}

// TestPutEmptySettlesTheMiss (#265): a completed "no preview" answer is
// RECORDED — KnownEmpty for that blob id — so the caller stops re-asking
// every frame. A changed blob id (the server minted a real preview)
// invalidates it, and a real image is never downgraded to a miss.
func TestPutEmptySettlesTheMiss(t *testing.T) {
	d := &fakeDecoder{}
	c := NewCache(d)

	c.PutEmpty("42", 7)
	if !c.KnownEmpty("42", 7) {
		t.Error("a settled empty answer must be known")
	}
	if _, ok := c.Get("42", 7); ok {
		t.Error("an empty answer is not an image")
	}
	if c.KnownEmpty("42", 8) {
		t.Error("a NEW blob id must invalidate the recorded miss (refetch)")
	}

	// A real Put supersedes; a later PutEmpty must not downgrade it.
	c.Put("42", 8, []byte("jpeg-bytes"), nil)
	d.resolveAll()
	c.PutEmpty("42", 8)
	if _, ok := c.Get("42", 8); !ok {
		t.Error("PutEmpty must never evict a decoded image")
	}
}

// TestPutGetRoundTrip: the obvious happy path. Put bytes under a
// known blob id, decode, Get with that blob id returns the same
// image.
func TestPutGetRoundTrip(t *testing.T) {
	d := &fakeDecoder{}
	c := NewCache(d)

	var ready bool
	c.Put("42", 7, []byte("jpeg-bytes"), func() { ready = true })
	if !ready && d.pendingCount() != 1 {
		t.Fatalf("expected one pending decode, got %d", d.pendingCount())
	}
	imgs := d.resolveAll()
	if !ready {
		t.Errorf("onReady never fired")
	}
	got, ok := c.Get("42", 7)
	if !ok {
		t.Fatalf("Get(42, 7) not ok after Put")
	}
	if got != imgs[0] {
		t.Errorf("Get returned a different image than was Put")
	}
}

// TestGetWithMismatchedBlobIDReturnsNotOK locks in the headline
// invariant: when the server says the tile's preview is now blob N+1
// but the cache still holds blob N, Get must miss so the renderer
// re-fetches. This is exactly the bug class that motivated the
// extraction.
func TestGetWithMismatchedBlobIDReturnsNotOK(t *testing.T) {
	d := &fakeDecoder{}
	c := NewCache(d)
	c.Put("42", 7, []byte("old"), nil)
	d.resolveAll()
	if _, ok := c.Get("42", 8); ok {
		t.Errorf("Get(42, 8) returned ok despite cached entry being blob 7")
	}
	// And the same blob id still hits — staleness is asymmetric.
	if _, ok := c.Get("42", 7); !ok {
		t.Errorf("Get(42, 7) missed despite cached entry being blob 7")
	}
}

// TestGetWithZeroBlobIDAlwaysMisses: a tile with PreviewBlobID == 0
// has no preview server-side. Callers must not see a stale cached
// image just because the cache happens to remember one.
func TestGetWithZeroBlobIDAlwaysMisses(t *testing.T) {
	d := &fakeDecoder{}
	c := NewCache(d)
	c.Put("42", 7, []byte("x"), nil)
	d.resolveAll()
	if _, ok := c.Get("42", 0); ok {
		t.Errorf("Get with wantBlobID=0 returned ok")
	}
}

// TestPutWildcardMatchesAnyBlobID covers the freeze / live-stream
// path: bytes captured locally before the server-side blob id is
// known must still satisfy renderer Gets regardless of what blob id
// the tile advertises.
func TestPutWildcardMatchesAnyBlobID(t *testing.T) {
	d := &fakeDecoder{}
	c := NewCache(d)
	c.PutWildcard("42", []byte("captured-locally"), nil)
	d.resolveAll()
	for _, want := range []int64{1, 99, 12345} {
		if _, ok := c.Get("42", want); !ok {
			t.Errorf("wildcard entry missed Get(42, %d)", want)
		}
	}
	// And wantBlobID=0 hits TOO: a tile that has never had a server-side
	// preview advertises PreviewBlobID 0, and the very first freeze parks
	// its frame under the wildcard before SetURLState/SetShellPreview
	// round-trips. If zero missed, the just-frozen frame stayed invisible
	// (placeholder glyph) until the SSE echo landed — the regression this
	// case pins. Zero-miss protection is only for entries keyed to a REAL
	// blob id (see TestGetWithZeroBlobIDAlwaysMisses): those are server
	// state that may be stale; a wildcard is a local capture that is by
	// definition fresher than the server.
	if _, ok := c.Get("42", 0); !ok {
		t.Errorf("wildcard entry missed Get(42, 0); the first-ever freeze of a tile must show immediately")
	}
}

// TestPutSupersedesPreviousImage: a second Put for the same tile
// installs the new image and revokes the old one. This is what makes
// "shell ascent updates the cache" work end-to-end.
func TestPutSupersedesPreviousImage(t *testing.T) {
	d := &fakeDecoder{}
	c := NewCache(d)
	c.Put("42", 1, []byte("first"), nil)
	imgs1 := d.resolveAll()
	c.Put("42", 2, []byte("second"), nil)
	imgs2 := d.resolveAll()
	if !imgs1[0].revoked.Load() {
		t.Errorf("first image was not revoked after second Put")
	}
	got, ok := c.Get("42", 2)
	if !ok || got != imgs2[0] {
		t.Errorf("Get(42, 2) did not return the second image")
	}
}

// TestPutLateResultIsDiscarded: when Put A's decode finishes AFTER
// Put B has been issued and resolved, A's late-arriving image must
// be revoked and not installed. Without generation tracking the cache
// would forget B and show A.
func TestPutLateResultIsDiscarded(t *testing.T) {
	d := &fakeDecoder{}
	c := NewCache(d)
	c.Put("42", 1, []byte("first"), nil)  // pending[0]
	c.Put("42", 2, []byte("second"), nil) // pending[1]
	// Resolve the second one first. It should install.
	imgSecond := d.resolve(1)
	got, ok := c.Get("42", 2)
	if !ok || got != imgSecond {
		t.Fatalf("Get did not return the in-order winner")
	}
	// Now the first decode finishes (late). It must be revoked and
	// must not displace the second.
	imgFirst := d.resolve(0)
	if !imgFirst.revoked.Load() {
		t.Errorf("late-arriving first decode was not revoked")
	}
	got, ok = c.Get("42", 2)
	if !ok || got != imgSecond {
		t.Errorf("late decode displaced the in-order winner")
	}
}

// TestPutWithEmptyBytesIsNoOp: defensive — a zero-length payload
// from a misbehaving caller (e.g. canvas.toDataURL returning nothing)
// must not poison the cache. Without this guard we'd queue a decode
// the Decoder would have to handle gracefully.
func TestPutWithEmptyBytesIsNoOp(t *testing.T) {
	d := &fakeDecoder{}
	c := NewCache(d)
	c.Put("42", 1, nil, nil)
	c.Put("42", 1, []byte{}, nil)
	if d.pendingCount() != 0 {
		t.Errorf("empty Put queued %d decodes; want 0", d.pendingCount())
	}
}

// TestPutDecodeErrorLeavesEntryUntouched: if the browser fails to
// decode (corrupt JPEG bytes), the existing cached image — if any —
// must survive. A decode failure shouldn't blank the screen.
func TestPutDecodeErrorLeavesEntryUntouched(t *testing.T) {
	d := &fakeDecoder{}
	c := NewCache(d)
	c.Put("42", 1, []byte("good"), nil)
	d.resolveAll()
	// Get baseline image.
	good, ok := c.Get("42", 1)
	if !ok {
		t.Fatal("setup: good Put didn't land")
	}
	// Now a Put whose decode will fail.
	c.Put("42", 2, []byte("corrupt"), nil)
	d.failNext(0)
	// The good image must still be present (under blob 1).
	got, ok := c.Get("42", 1)
	if !ok || got != good {
		t.Errorf("decode failure clobbered the prior good entry")
	}
}

// TestDropRemovesEntry: deleting a tile drops its cache row and
// revokes the image. Idempotent: a second Drop on the same tile is a
// no-op.
func TestDropRemovesEntry(t *testing.T) {
	d := &fakeDecoder{}
	c := NewCache(d)
	c.Put("42", 1, []byte("x"), nil)
	imgs := d.resolveAll()
	c.Drop("42")
	if !imgs[0].revoked.Load() {
		t.Errorf("Drop did not revoke the image")
	}
	if _, ok := c.Get("42", 1); ok {
		t.Errorf("Get after Drop returned ok")
	}
	c.Drop("42") // must not panic
}

// TestMarkFetchingExcludesDuplicateCallers: the cache is also the
// in-flight set for GetTilePreview round-trips. Two concurrent
// fetchers must collapse into one network request.
func TestMarkFetchingExcludesDuplicateCallers(t *testing.T) {
	c := NewCache(&fakeDecoder{})
	if !c.MarkFetching("42") {
		t.Errorf("first MarkFetching returned false")
	}
	if c.MarkFetching("42") {
		t.Errorf("second MarkFetching for same tile returned true; want false")
	}
	c.ClearFetching("42")
	if !c.MarkFetching("42") {
		t.Errorf("MarkFetching after ClearFetching returned false")
	}
	c.ClearFetching("42")
	c.ClearFetching("42") // must not panic
}

// TestMarkFetchingIsPerTile: different tiles must not block each
// other's fetches.
func TestMarkFetchingIsPerTile(t *testing.T) {
	c := NewCache(&fakeDecoder{})
	if !c.MarkFetching("1") || !c.MarkFetching("2") {
		t.Errorf("MarkFetching on distinct tiles refused the second")
	}
}

// TestGetWhileDecodingReturnsNotOK: between Put and the decoder's
// onReady, Get must not return a stale or zero image. The cache
// only flips to ok once decode is installed.
func TestGetWhileDecodingReturnsNotOK(t *testing.T) {
	d := &fakeDecoder{}
	c := NewCache(d)
	c.Put("42", 1, []byte("pending"), nil)
	// Decode not yet resolved.
	if _, ok := c.Get("42", 1); ok {
		t.Errorf("Get returned ok while decode was still pending")
	}
	d.resolveAll()
	if _, ok := c.Get("42", 1); !ok {
		t.Errorf("Get missed after decode completed")
	}
}

// TestRevokedImageReportsNotTruthyAndGetMisses: an entry whose Image
// has been revoked externally must not satisfy Get, so a leaked
// previous-decode image can't get drawn after teardown.
func TestRevokedImageReportsNotTruthyAndGetMisses(t *testing.T) {
	d := &fakeDecoder{}
	c := NewCache(d)
	c.Put("42", 1, []byte("x"), nil)
	imgs := d.resolveAll()
	imgs[0].Revoke()
	if _, ok := c.Get("42", 1); ok {
		t.Errorf("Get returned ok for a revoked image")
	}
}
