package preview

import (
	"sync"
	"sync/atomic"
	"testing"
)

// fakeImage records bytes and revoked state so tests can assert that resources
// are released.
type fakeImage struct {
	bytes   []byte
	revoked atomic.Bool
}

func (i *fakeImage) Truthy() bool { return !i.revoked.Load() }
func (i *fakeImage) Revoke()      { i.revoked.Store(true) }

// fakeDecoder buffers Decode calls so tests can resolve them in any order. A
// synchronous decoder would always resolve in call order and hide the
// out-of-order Put case.
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

// resolveAll fires onReady for every queued decode in order and returns the
// images, so tests can inspect Revoke calls on them.
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

// resolve fires the i-th queued decode alone, leaving later decodes pending.
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

// TestGetEmptyReturnsNotOK pins that a new cache has nothing in it.
func TestGetEmptyReturnsNotOK(t *testing.T) {
	c := NewCache(&fakeDecoder{}, nil)
	if _, ok := c.Get("42", 1); ok {
		t.Errorf("Get on empty cache returned ok")
	}
}

// TestPutEmptySettlesTheMiss pins that a completed no-preview answer becomes
// KnownEmpty for that blob id, so the caller stops re-asking every frame. A
// changed blob id invalidates it, and a real image is never downgraded to a
// miss.
func TestPutEmptySettlesTheMiss(t *testing.T) {
	d := &fakeDecoder{}
	c := NewCache(d, nil)

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

	// A real Put supersedes, and a later PutEmpty does not downgrade it.
	c.Put("42", 8, []byte("jpeg-bytes"), nil)
	d.resolveAll()
	c.PutEmpty("42", 8)
	if _, ok := c.Get("42", 8); !ok {
		t.Error("PutEmpty must never evict a decoded image")
	}
}

// TestPutGetRoundTrip pins the happy path: bytes put under a blob id decode,
// and Get with that blob id returns the same image.
func TestPutGetRoundTrip(t *testing.T) {
	d := &fakeDecoder{}
	c := NewCache(d, nil)

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

// TestGetWithMismatchedBlobIDReturnsNotOK pins the invalidation rule: when the
// server says the preview is now blob N+1 and the cache holds blob N, Get
// misses so the renderer re-fetches.
func TestGetWithMismatchedBlobIDReturnsNotOK(t *testing.T) {
	d := &fakeDecoder{}
	c := NewCache(d, nil)
	c.Put("42", 7, []byte("old"), nil)
	d.resolveAll()
	if _, ok := c.Get("42", 8); ok {
		t.Errorf("Get(42, 8) returned ok despite cached entry being blob 7")
	}
	// The same blob id still hits, so staleness is asymmetric.
	if _, ok := c.Get("42", 7); !ok {
		t.Errorf("Get(42, 7) missed despite cached entry being blob 7")
	}
}

// TestGetWithZeroBlobIDAlwaysMisses pins that a tile with PreviewBlobID 0 has
// no server-side preview, so a caller does not see a cached image the server
// says is not there.
func TestGetWithZeroBlobIDAlwaysMisses(t *testing.T) {
	d := &fakeDecoder{}
	c := NewCache(d, nil)
	c.Put("42", 7, []byte("x"), nil)
	d.resolveAll()
	if _, ok := c.Get("42", 0); ok {
		t.Errorf("Get with wantBlobID=0 returned ok")
	}
}

// TestPutWildcardMatchesAnyBlobID covers the freeze and live-stream path: bytes
// captured before the server-side blob id is known still satisfy renderer Gets
// whatever blob id the tile advertises.
func TestPutWildcardMatchesAnyBlobID(t *testing.T) {
	d := &fakeDecoder{}
	c := NewCache(d, nil)
	c.PutWildcard("42", []byte("captured-locally"), nil)
	d.resolveAll()
	for _, want := range []int64{1, 99, 12345} {
		if _, ok := c.Get("42", want); !ok {
			t.Errorf("wildcard entry missed Get(42, %d)", want)
		}
	}
	// A wantBlobID of 0 hits too. A tile that has never had a server-side
	// preview advertises PreviewBlobID 0, and the first freeze parks its frame
	// under the wildcard before SetURLState or SetShellPreview round-trips, so
	// a miss would leave the just-frozen frame as a placeholder glyph. The
	// zero-miss rule covers only entries keyed to a real blob id, see
	// TestGetWithZeroBlobIDAlwaysMisses, because those are server state that
	// may be stale while a wildcard is a fresher local capture.
	if _, ok := c.Get("42", 0); !ok {
		t.Errorf("wildcard entry missed Get(42, 0); the first-ever freeze of a tile must show immediately")
	}
}

// TestPutSupersedesPreviousImage pins that a second Put for the same tile
// installs the new image and revokes the old one, which is how a shell ascent
// updates the cache.
func TestPutSupersedesPreviousImage(t *testing.T) {
	d := &fakeDecoder{}
	c := NewCache(d, nil)
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

// TestPutLateResultIsDiscarded pins that when the first Put's decode finishes
// after a second Put has resolved, the late image is revoked and not installed.
// Without the generation counter the cache would forget the second and show the
// first.
func TestPutLateResultIsDiscarded(t *testing.T) {
	d := &fakeDecoder{}
	c := NewCache(d, nil)
	c.Put("42", 1, []byte("first"), nil)  // pending[0]
	c.Put("42", 2, []byte("second"), nil) // pending[1]
	// The second resolves first and installs.
	imgSecond := d.resolve(1)
	got, ok := c.Get("42", 2)
	if !ok || got != imgSecond {
		t.Fatalf("Get did not return the in-order winner")
	}
	// The first decode finishes late, so it is revoked and does not displace
	// the second.
	imgFirst := d.resolve(0)
	if !imgFirst.revoked.Load() {
		t.Errorf("late-arriving first decode was not revoked")
	}
	got, ok = c.Get("42", 2)
	if !ok || got != imgSecond {
		t.Errorf("late decode displaced the in-order winner")
	}
}

// TestPutWithEmptyBytesIsNoOp pins that a zero-length payload does not reach
// the cache or the Decoder.
func TestPutWithEmptyBytesIsNoOp(t *testing.T) {
	d := &fakeDecoder{}
	c := NewCache(d, nil)
	c.Put("42", 1, nil, nil)
	c.Put("42", 1, []byte{}, nil)
	if d.pendingCount() != 0 {
		t.Errorf("empty Put queued %d decodes; want 0", d.pendingCount())
	}
}

// TestPutDecodeErrorLeavesEntryUntouched pins that a failed decode leaves any
// existing cached image in place, so a corrupt JPEG does not blank the screen.
func TestPutDecodeErrorLeavesEntryUntouched(t *testing.T) {
	d := &fakeDecoder{}
	c := NewCache(d, nil)
	c.Put("42", 1, []byte("good"), nil)
	d.resolveAll()
	good, ok := c.Get("42", 1)
	if !ok {
		t.Fatal("setup: good Put didn't land")
	}
	// A Put whose decode fails.
	c.Put("42", 2, []byte("corrupt"), nil)
	d.failNext(0)
	// The good image is still there under blob 1.
	got, ok := c.Get("42", 1)
	if !ok || got != good {
		t.Errorf("decode failure clobbered the prior good entry")
	}
	// And blob 2 is settled, so the caller stops asking for bytes that will
	// not decode. A prior image does not exempt the tile from the loop: Get
	// misses for blob 2 whatever is held for blob 1.
	if !c.KnownEmpty("42", 2) {
		t.Errorf("decode failure left blob 2 unsettled; the caller re-fetches every draw")
	}
}

// TestDropRemovesEntry pins that deleting a tile drops its cache row, revokes
// the image, and takes a second Drop without complaint.
func TestDropRemovesEntry(t *testing.T) {
	d := &fakeDecoder{}
	c := NewCache(d, nil)
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

// TestGetWhileDecodingReturnsNotOK pins that between Put and onReady, Get
// misses. The cache turns ok only once the decode is installed.
func TestGetWhileDecodingReturnsNotOK(t *testing.T) {
	d := &fakeDecoder{}
	c := NewCache(d, nil)
	c.Put("42", 1, []byte("pending"), nil)
	if _, ok := c.Get("42", 1); ok {
		t.Errorf("Get returned ok while decode was still pending")
	}
	d.resolveAll()
	if _, ok := c.Get("42", 1); !ok {
		t.Errorf("Get missed after decode completed")
	}
}

// TestRevokedImageReportsNotTruthyAndGetMisses pins that an entry whose Image
// was revoked elsewhere does not satisfy Get, so a leaked image is not drawn
// after teardown.
func TestRevokedImageReportsNotTruthyAndGetMisses(t *testing.T) {
	d := &fakeDecoder{}
	c := NewCache(d, nil)
	c.Put("42", 1, []byte("x"), nil)
	imgs := d.resolveAll()
	imgs[0].Revoke()
	if _, ok := c.Get("42", 1); ok {
		t.Errorf("Get returned ok for a revoked image")
	}
}

// TestPutDecodeErrorSettlesTheMiss pins that a decode failure on a tile with
// no prior image is a settled answer for that blob id, reported once. Left
// unsettled, Get misses and KnownEmpty is false forever, so the caller's fetch
// guard re-asks the server on every draw, one RPC per frame, silently.
func TestPutDecodeErrorSettlesTheMiss(t *testing.T) {
	d := &fakeDecoder{}
	var reported []string
	c := NewCache(d, func(tileID string) { reported = append(reported, tileID) })

	c.Put("42", 7, []byte("corrupt"), nil)
	d.failNext(0)
	if _, ok := c.Get("42", 7); ok {
		t.Error("a failed decode is not an image")
	}
	if !c.KnownEmpty("42", 7) {
		t.Error("a failed decode must settle the miss for its blob id")
	}
	if len(reported) != 1 || reported[0] != "42" {
		t.Errorf("decode failure reported %v; want one report for tile 42", reported)
	}

	// A new blob id is a new question, and a Put that decodes answers it.
	if c.KnownEmpty("42", 8) {
		t.Error("a NEW blob id must not inherit the failed blob's miss")
	}
	c.Put("42", 8, []byte("good"), nil)
	d.resolveAll()
	if _, ok := c.Get("42", 8); !ok {
		t.Fatal("a decoding Put after a failed one must install")
	}
	if c.KnownEmpty("42", 8) {
		t.Error("an installed image is not a miss")
	}
}

// TestPutDecodeErrorFromSupersededPutIsIgnored pins that a late failure from a
// Put a newer one replaced records nothing and says nothing: the newer Put
// owns the entry's answer.
func TestPutDecodeErrorFromSupersededPutIsIgnored(t *testing.T) {
	d := &fakeDecoder{}
	var reported []string
	c := NewCache(d, func(tileID string) { reported = append(reported, tileID) })
	c.Put("42", 1, []byte("corrupt"), nil) // pending[0]
	c.Put("42", 2, []byte("good"), nil)    // pending[1]
	img := d.resolve(1)
	d.failNext(0)
	got, ok := c.Get("42", 2)
	if !ok || got != img {
		t.Error("a superseded decode failure displaced the winner")
	}
	if c.KnownEmpty("42", 1) {
		t.Error("a superseded decode failure recorded a miss")
	}
	if len(reported) != 0 {
		t.Errorf("a superseded decode failure reported %v; want nothing", reported)
	}
}
