package rasterprev

import "testing"

type fakeRaster struct {
	svg     string
	revoked bool
}

func (r *fakeRaster) Truthy() bool { return !r.revoked }
func (r *fakeRaster) Revoke()      { r.revoked = true }

// fakeRasterizer queues calls so a test resolves them in any order. A
// synchronous one would always resolve in call order and hide the superseded
// case.
type fakeRasterizer struct {
	pending []pending
	calls   int
}

type pending struct {
	svg     string
	onReady func(Raster)
	onError func()
}

func (f *fakeRasterizer) Rasterize(svg string, onReady func(Raster), onError func()) {
	f.calls++
	f.pending = append(f.pending, pending{svg, onReady, onError})
}

func (f *fakeRasterizer) resolve(i int) *fakeRaster {
	p := f.pending[i]
	f.pending = append(f.pending[:i], f.pending[i+1:]...)
	r := &fakeRaster{svg: p.svg}
	p.onReady(r)
	return r
}

func (f *fakeRasterizer) fail(i int) {
	p := f.pending[i]
	f.pending = append(f.pending[:i], f.pending[i+1:]...)
	p.onError()
}

func svgOK(s string) func() (string, bool) {
	return func() (string, bool) { return s, true }
}

func key(bytes uint64, bucket float64) Key {
	return Key{TileID: "t1", Bytes: bytes, Bucket: bucket}
}

// A bucket rounds down, so a raster drawn at the text's scale is never wider
// than the box that asked for it, and never narrower by a step or more.
func TestBucketQuantizes(t *testing.T) {
	for _, c := range []struct{ in, want float64 }{{0, 64}, {10, 64}, {64, 64}, {71.9, 64}, {72, 72}, {100, 91}, {1e9, 72277}} {
		if got := Bucket(c.in); got != c.want {
			t.Errorf("Bucket(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	steps := map[float64]bool{}
	for w := 64.0; w < 4000; w += 0.5 {
		b := Bucket(w)
		if b > w || b*9/8 <= w {
			t.Fatalf("Bucket(%v) = %v: outside (w*8/9, w]", w, b)
		}
		steps[b] = true
	}
	if len(steps) > 40 {
		t.Errorf("%d buckets between 64 and 4000 px: a zoom would re-rasterize too often", len(steps))
	}
}

func TestFailureReportsOncePerKeyAndNeverRetries(t *testing.T) {
	ras := &fakeRasterizer{}
	var reports []string
	c := NewCache(ras, func(id string) { reports = append(reports, id) })

	if _, _, ok := c.Ensure(key(1, 64), svgOK("<svg/>"), nil); ok {
		t.Fatal("a pending raster must not report ready")
	}
	ras.fail(0)
	if len(reports) != 1 {
		t.Fatalf("reports after failure = %v, want one", reports)
	}
	// Later frames re-ask for the same key.
	for i := 0; i < 5; i++ {
		if _, _, ok := c.Ensure(key(1, 64), svgOK("<svg/>"), nil); ok {
			t.Fatal("a failed key must stay not-ready")
		}
	}
	if len(reports) != 1 {
		t.Errorf("reports after 5 more frames = %v, want the one", reports)
	}
	if ras.calls != 1 {
		t.Errorf("Rasterize calls = %d, want 1: a failed key must not retry-loop", ras.calls)
	}
	if st := c.States()["t1"]; !st.Failed || st.Ready {
		t.Errorf("States()[t1] = %+v, want failed and not ready", st)
	}
}

func TestNewBytesClearFailedState(t *testing.T) {
	ras := &fakeRasterizer{}
	var reports []string
	c := NewCache(ras, func(id string) { reports = append(reports, id) })

	c.Ensure(key(1, 64), svgOK("v1"), nil)
	ras.fail(0)

	var readies int
	if _, _, ok := c.Ensure(key(2, 64), svgOK("v2"), func() { readies++ }); ok {
		t.Fatal("the new bytes' raster is not loaded yet")
	}
	if ras.calls != 2 {
		t.Fatalf("Rasterize calls = %d, want 2: new bytes re-rasterize", ras.calls)
	}
	ras.resolve(0)
	if readies != 1 {
		t.Errorf("onReady fired %d times, want 1", readies)
	}
	r, _, ok := c.Ensure(key(2, 64), svgOK("v2"), nil)
	if !ok || r.(*fakeRaster).svg != "v2" {
		t.Fatalf("Ensure after resolve = %v, %v; want the v2 raster", r, ok)
	}
	if st := c.States()["t1"]; st.Failed || !st.Ready {
		t.Errorf("States()[t1] = %+v, want ready and not failed", st)
	}
	if len(reports) != 1 {
		t.Errorf("reports = %v, want only the v1 failure", reports)
	}
}

func TestSupersededFailureIsNotReported(t *testing.T) {
	ras := &fakeRasterizer{}
	var reports []string
	c := NewCache(ras, func(id string) { reports = append(reports, id) })

	c.Ensure(key(1, 64), svgOK("v1"), nil)
	c.Ensure(key(2, 64), svgOK("v2"), nil)
	ras.fail(0) // v1's late verdict, after v2 took the slot
	if len(reports) != 0 {
		t.Errorf("reports = %v, want none: the newer rasterization owns the answer", reports)
	}
	if st := c.States()["t1"]; st.Failed {
		t.Errorf("States()[t1] = %+v, want the superseded failure ignored", st)
	}
}

func TestOtherBucketsSurviveTheSameBytesAndSweepOnNewOnes(t *testing.T) {
	ras := &fakeRasterizer{}
	c := NewCache(ras, nil)

	c.Ensure(key(1, 64), svgOK("a"), nil)
	c.Ensure(key(1, 128), svgOK("b"), nil)
	wide := ras.resolve(1)
	narrow := ras.resolve(0)
	if _, _, ok := c.Ensure(key(1, 64), svgOK("a"), nil); !ok {
		t.Fatal("the 64 bucket must survive a draw at 128")
	}
	if narrow.revoked || wide.revoked {
		t.Fatal("two buckets of one body must not revoke each other")
	}

	// New bytes at one bucket retire the other bucket's picture.
	c.Ensure(key(2, 64), svgOK("a2"), nil)
	if !wide.revoked {
		t.Error("a bucket whose bytes moved on must be swept and revoked")
	}
	ras.resolve(0)
	if !narrow.revoked {
		t.Error("the replaced same-bucket raster must be revoked once its successor lands")
	}
	if _, ok := c.States()["t1"]; !ok {
		t.Error("the tile keeps its new pending entry")
	}
}

func TestBuildMissCachesNothing(t *testing.T) {
	ras := &fakeRasterizer{}
	c := NewCache(ras, nil)
	if _, _, ok := c.Ensure(key(1, 64), func() (string, bool) { return "", false }, nil); ok {
		t.Fatal("a tile whose bytes are not loaded has no raster")
	}
	if ras.calls != 0 {
		t.Fatalf("Rasterize calls = %d, want 0", ras.calls)
	}
	if len(c.States()) != 0 {
		t.Errorf("States() = %v, want empty", c.States())
	}
}

func TestDropRevokesEveryBucket(t *testing.T) {
	ras := &fakeRasterizer{}
	c := NewCache(ras, nil)
	c.Ensure(key(1, 64), svgOK("a"), nil)
	c.Ensure(key(1, 128), svgOK("b"), nil)
	c.Ensure(Key{TileID: "t2", Bytes: 1, Bucket: 64}, svgOK("c"), nil)
	a := ras.resolve(0)
	b := ras.resolve(0)
	other := ras.resolve(0)

	c.Drop("t1")
	if !a.revoked || !b.revoked {
		t.Error("Drop must revoke every bucket of the dropped tile")
	}
	if other.revoked {
		t.Error("Drop must leave another tile's raster alone")
	}
	if _, ok := c.States()["t1"]; ok {
		t.Errorf("States() = %v, want t1 gone", c.States())
	}
	c.Drop("t1") // idempotent
}

func TestLateResultAfterDropIsRevoked(t *testing.T) {
	ras := &fakeRasterizer{}
	c := NewCache(ras, nil)
	c.Ensure(key(1, 64), svgOK("a"), nil)
	c.Drop("t1")
	r := ras.resolve(0)
	if !r.revoked {
		t.Error("a raster landing after Drop must be revoked, not leaked")
	}
	if len(c.States()) != 0 {
		t.Errorf("States() = %v, want empty", c.States())
	}
}

// A zoom that crosses into a bucket with no raster yet keeps drawing the
// document from the nearest ready bucket of the same bytes, never raw
// source and never other bytes' picture.
func TestStandInAcrossBucketsNeverAcrossBytes(t *testing.T) {
	ras := &fakeRasterizer{}
	c := NewCache(ras, nil)
	c.Ensure(key(1, 128), svgOK("v1@128"), nil)
	c.Ensure(key(1, 320), svgOK("v1@320"), nil)
	ras.resolve(0)
	ras.resolve(0)

	r, w, ok := c.Ensure(key(1, 192), svgOK("v1@192"), nil)
	if !ok || r.(*fakeRaster).svg != "v1@128" || w != 128 {
		t.Fatalf("in flight at 192: %v %v %v, want the nearest ready bucket, 128", r, w, ok)
	}
	if r, w, ok := c.Ensure(key(1, 192), svgOK("v1@192"), nil); !ok || w != 128 || r.(*fakeRaster).svg != "v1@128" {
		t.Fatalf("second frame in flight: %v %v %v, want the stand-in again", r, w, ok)
	}
	if ras.calls != 3 {
		t.Errorf("Rasterize calls = %d, want 3: a stand-in does not re-ask", ras.calls)
	}
	ras.resolve(0)
	if r, w, ok := c.Ensure(key(1, 192), svgOK("v1@192"), nil); !ok || w != 192 || r.(*fakeRaster).svg != "v1@192" {
		t.Fatalf("landed: %v %v %v, want 192's own raster", r, w, ok)
	}

	// New bytes: the old bytes' buckets are no stand-in.
	if r, _, ok := c.Ensure(key(2, 256), svgOK("v2@256"), nil); ok {
		t.Fatalf("v2 in flight served %v: a stand-in crosses bytes only in its own slot", r.(*fakeRaster).svg)
	}
	ras.resolve(0)
	if r, w, ok := c.Ensure(key(2, 128), svgOK("v2@128"), nil); !ok || w != 256 || r.(*fakeRaster).svg != "v2@256" {
		t.Fatalf("v2 at 128 in flight: %v %v %v, want v2's 256", r, w, ok)
	}

	// A theme change is a different picture too.
	other := key(2, 128)
	other.Theme = "light"
	if _, _, ok := c.Ensure(other, svgOK("light"), nil); ok {
		t.Error("a stand-in must never cross themes")
	}
}

// A failed key paints raw source and says so; it does not hide behind a
// neighbouring bucket.
func TestFailedKeyGetsNoStandIn(t *testing.T) {
	ras := &fakeRasterizer{}
	c := NewCache(ras, nil)
	c.Ensure(key(1, 128), svgOK("a"), nil)
	ras.resolve(0)
	c.Ensure(key(1, 192), svgOK("b"), nil)
	ras.fail(0)
	if _, _, ok := c.Ensure(key(1, 192), svgOK("b"), nil); ok {
		t.Error("a failed key answered with a stand-in")
	}
}

func TestStandInTieGoesToTheNarrower(t *testing.T) {
	ras := &fakeRasterizer{}
	c := NewCache(ras, nil)
	c.Ensure(key(1, 128), svgOK("128"), nil)
	c.Ensure(key(1, 256), svgOK("256"), nil)
	ras.resolve(0)
	ras.resolve(0)
	if _, w, ok := c.Ensure(key(1, 192), svgOK("192"), nil); !ok || w != 128 {
		t.Errorf("tie: width %v ok %v, want 128, which cannot clip", w, ok)
	}
}

// Typing moves the bytes on every keystroke, so a face rasterizes again for
// each. Until the new picture lands, the slot's last picture stands in, and
// never longer: the landing or the failure retires it. Without it, a face of a
// document being typed elsewhere would flash raw source per keystroke.
func TestASlotsLastPictureStandsInOnlyWhileItsSuccessorIsInFlight(t *testing.T) {
	ras := &fakeRasterizer{}
	c := NewCache(ras, nil)
	c.Ensure(key(1, 128), svgOK("v1"), nil)
	v1 := ras.resolve(0)

	if r, w, ok := c.Ensure(key(2, 128), svgOK("v2"), nil); !ok || w != 128 || r != v1 {
		t.Fatalf("v2 in flight: %v %v %v, want v1 standing in", r, w, ok)
	}
	// A third edit before the second lands keeps the last landed picture.
	if r, _, ok := c.Ensure(key(3, 128), svgOK("v3"), nil); !ok || r != v1 {
		t.Fatalf("v3 in flight over v2 in flight: %v %v, want v1 still", r, ok)
	}
	if v1.revoked {
		t.Fatal("the standing-in picture was revoked while it stands in")
	}
	ras.resolve(0) // v2 lands late: superseded
	if r, _, ok := c.Ensure(key(3, 128), svgOK("v3"), nil); !ok || r != v1 {
		t.Fatalf("after the superseded v2 landed: %v %v, want v1 until v3 lands", r, ok)
	}
	v3 := ras.resolve(0)
	if r, _, ok := c.Ensure(key(3, 128), svgOK("v3"), nil); !ok || r != v3 {
		t.Fatalf("v3 landed: %v %v, want v3", r, ok)
	}
	if !v1.revoked {
		t.Error("the stand-in outlived its successor's landing")
	}

	// A failure retires the stand-in too: the face says raw, not old bytes.
	c.Ensure(key(4, 128), svgOK("v4"), nil)
	ras.fail(0)
	if r, _, ok := c.Ensure(key(4, 128), svgOK("v4"), nil); ok {
		t.Fatalf("v4 failed and %v stood in", r.(*fakeRaster).svg)
	}
	if !v3.revoked {
		t.Error("the stand-in outlived its successor's failure")
	}

	// Another theme is another picture, never a stand-in.
	c.Ensure(key(5, 128), svgOK("v5"), nil)
	ras.resolve(0)
	other := key(6, 128)
	other.Theme = "light"
	if _, _, ok := c.Ensure(other, svgOK("light"), nil); ok {
		t.Error("a stand-in crossed themes")
	}
}
