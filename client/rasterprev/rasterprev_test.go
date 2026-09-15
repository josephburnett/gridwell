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

func key(version int64, bucket float64) Key {
	return Key{TileID: "t1", Version: version, Bucket: bucket}
}

func TestBucketQuantizes(t *testing.T) {
	for _, c := range []struct{ in, want float64 }{{0, 64}, {10, 64}, {100, 128}, {200, 192}} {
		if got := Bucket(c.in); got != c.want {
			t.Errorf("Bucket(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestFailureReportsOncePerKeyAndNeverRetries(t *testing.T) {
	ras := &fakeRasterizer{}
	var reports []string
	c := NewCache(ras, func(id string) { reports = append(reports, id) })

	if _, ok := c.Ensure(key(1, 64), svgOK("<svg/>"), nil); ok {
		t.Fatal("a pending raster must not report ready")
	}
	ras.fail(0)
	if len(reports) != 1 {
		t.Fatalf("reports after failure = %v, want one", reports)
	}
	// Later frames re-ask for the same key.
	for i := 0; i < 5; i++ {
		if _, ok := c.Ensure(key(1, 64), svgOK("<svg/>"), nil); ok {
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

func TestNewVersionClearsFailedState(t *testing.T) {
	ras := &fakeRasterizer{}
	var reports []string
	c := NewCache(ras, func(id string) { reports = append(reports, id) })

	c.Ensure(key(1, 64), svgOK("v1"), nil)
	ras.fail(0)

	var readies int
	if _, ok := c.Ensure(key(2, 64), svgOK("v2"), func() { readies++ }); ok {
		t.Fatal("the new version's raster is not loaded yet")
	}
	if ras.calls != 2 {
		t.Fatalf("Rasterize calls = %d, want 2: a new version re-rasterizes", ras.calls)
	}
	ras.resolve(0)
	if readies != 1 {
		t.Errorf("onReady fired %d times, want 1", readies)
	}
	r, ok := c.Ensure(key(2, 64), svgOK("v2"), nil)
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

func TestOtherBucketsSurviveTheSameVersionAndSweepOnANewOne(t *testing.T) {
	ras := &fakeRasterizer{}
	c := NewCache(ras, nil)

	c.Ensure(key(1, 64), svgOK("a"), nil)
	c.Ensure(key(1, 128), svgOK("b"), nil)
	wide := ras.resolve(1)
	narrow := ras.resolve(0)
	if _, ok := c.Ensure(key(1, 64), svgOK("a"), nil); !ok {
		t.Fatal("the 64 bucket must survive a draw at 128")
	}
	if narrow.revoked || wide.revoked {
		t.Fatal("two buckets of one version must not revoke each other")
	}

	// New bytes at one bucket retire the other bucket's picture.
	c.Ensure(key(2, 64), svgOK("a2"), nil)
	if !wide.revoked {
		t.Error("a bucket whose version moved on must be swept and revoked")
	}
	if !narrow.revoked {
		t.Error("the replaced same-bucket raster must be revoked")
	}
	if _, ok := c.States()["t1"]; !ok {
		t.Error("the tile keeps its new pending entry")
	}
}

func TestBuildMissCachesNothing(t *testing.T) {
	ras := &fakeRasterizer{}
	c := NewCache(ras, nil)
	if _, ok := c.Ensure(key(1, 64), func() (string, bool) { return "", false }, nil); ok {
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
	c.Ensure(Key{TileID: "t2", Version: 1, Bucket: 64}, svgOK("c"), nil)
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
