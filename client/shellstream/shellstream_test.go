package shellstream

import (
	"reflect"
	"testing"
)

type fakeStream struct {
	tileID  string
	cols    int
	rows    int
	writes  [][]byte
	resizes [][2]int
	closed  bool
	onData  func([]byte)
	onEnd   func(string, bool)
}

func (f *fakeStream) Write(d []byte)        { f.writes = append(f.writes, d) }
func (f *fakeStream) Resize(cols, rows int) { f.resizes = append(f.resizes, [2]int{cols, rows}) }
func (f *fakeStream) Close()                { f.closed = true }

type harness struct {
	reg    *Registry
	dialed []*fakeStream
	data   []struct {
		tileID string
		bytes  []byte
	}
	exits []Exit
}

func newHarness() *harness {
	h := &harness{}
	dial := func(tileID string, cols, rows int, onData func([]byte), onEnd func(string, bool)) Handle {
		f := &fakeStream{tileID: tileID, cols: cols, rows: rows, onData: onData, onEnd: onEnd}
		h.dialed = append(h.dialed, f)
		return f
	}
	h.reg = New(dial,
		func(tileID string, b []byte) {
			h.data = append(h.data, struct {
				tileID string
				bytes  []byte
			}{tileID, b})
		},
		func(e Exit) { h.exits = append(h.exits, e) })
	return h
}

func TestOpenReplacesTheStreamForTheTile(t *testing.T) {
	h := newHarness()
	h.reg.Open("u/1", 80, 24)
	h.reg.Open("u/1", 80, 24)
	if len(h.dialed) != 2 {
		t.Fatalf("dialed %d streams, want 2", len(h.dialed))
	}
	if !h.dialed[0].closed || h.dialed[1].closed {
		t.Fatal("the old stream must close and the new one must not")
	}
	h.reg.Write("u/1", []byte{1})
	if len(h.dialed[0].writes) != 0 || len(h.dialed[1].writes) != 1 {
		t.Fatal("writes must reach the replacement, never the closed stream")
	}
}

func TestLateBytesFromAReplacedStreamNeverReachTheRenderer(t *testing.T) {
	h := newHarness()
	h.reg.Open("u/1", 80, 24)
	old := h.dialed[0]
	h.reg.Open("u/1", 80, 24)
	old.onData([]byte{1, 2, 3}) // straggler from the torn-down PTY
	h.dialed[1].onData([]byte{9})
	if len(h.data) != 1 || !reflect.DeepEqual(h.data[0].bytes, []byte{9}) {
		t.Fatalf("renderer saw %v, want only the new stream's bytes", h.data)
	}
}

func TestExitFiresExactlyOnce(t *testing.T) {
	h := newHarness()
	h.reg.Open("u/1", 80, 24)
	h.dialed[0].onEnd("boom", false)
	h.dialed[0].onEnd("", false) // a failure and a close can both arrive
	if len(h.exits) != 1 || h.exits[0].Message != "boom" {
		t.Fatalf("exits = %+v, want exactly one 'boom'", h.exits)
	}
	h.reg.Write("u/1", []byte{1})
	if len(h.dialed[0].writes) != 0 {
		t.Fatal("a write after the exit must reach nothing")
	}
}

func TestLocalCloseSuppressesTheExitReport(t *testing.T) {
	h := newHarness()
	h.reg.Open("u/1", 80, 24)
	h.reg.Close("u/1")
	if !h.dialed[0].closed {
		t.Fatal("close must reach the stream")
	}
	h.dialed[0].onEnd("", false) // the end still arrives after a local close
	if len(h.exits) != 0 {
		t.Fatalf("this side asked: no exit report, got %+v", h.exits)
	}
}

func TestReplacedStreamsLateEndNeverFreezesTheNewStream(t *testing.T) {
	h := newHarness()
	h.reg.Open("u/1", 80, 24)
	old := h.dialed[0]
	h.reg.Open("u/1", 80, 24) // re-attach (the refresh gesture)
	old.onEnd("", false)      // the torn-down stream's end arrives late
	if len(h.exits) != 0 {
		t.Fatalf("a replaced stream's end must not freeze the tile: %+v", h.exits)
	}
	h.reg.Write("u/1", []byte{1})
	if len(h.dialed[1].writes) != 1 {
		t.Fatal("the new stream is still the tile's")
	}
}

func TestWriteAndResizeAfterCloseAreNoOps(t *testing.T) {
	h := newHarness()
	h.reg.Open("u/1", 80, 24)
	h.reg.Close("u/1")
	h.reg.Write("u/1", []byte{1})
	h.reg.Resize("u/1", 100, 30)
	if len(h.dialed[0].writes) != 0 || len(h.dialed[0].resizes) != 0 {
		t.Fatal("a race between teardown and an in-flight keystroke is expected, not an error")
	}
	// A tile that never opened is the same no-op.
	h.reg.Write("nobody", []byte{1})
	h.reg.Resize("nobody", 10, 10)
}

func TestSessionGoneRidesTheExit(t *testing.T) {
	h := newHarness()
	h.reg.Open("u/1", 80, 24)
	h.dialed[0].onEnd("session gone", true)
	if len(h.exits) != 1 || !h.exits[0].SessionGone {
		t.Fatalf("the gone verdict must ride the exit: %+v", h.exits)
	}
}

func TestTwoTilesHoldIndependentStreams(t *testing.T) {
	h := newHarness()
	h.reg.Open("u/1", 80, 24)
	h.reg.Open("u/2", 80, 24)
	h.reg.Write("u/2", []byte{5})
	if len(h.dialed[0].writes) != 0 || len(h.dialed[1].writes) != 1 {
		t.Fatal("tiles must not share a stream")
	}
	h.reg.Close("u/1")
	h.reg.Close("u/2")
	if !h.dialed[0].closed || !h.dialed[1].closed {
		t.Fatal("each tile closes its own stream")
	}
}

// A dial that fails instantly still reports, so the pane does not sit forever
// on a stream that never opened. An always-asynchronous dialer hides this case.
func TestASynchronousDialFailureIsReported(t *testing.T) {
	var closed bool
	reg := New(
		func(_ string, _, _ int, _ func([]byte), onEnd func(string, bool)) Handle {
			onEnd("dial refused", false)
			return &fakeStream{}
		},
		func(string, []byte) {},
		func(e Exit) { closed = true; _ = e },
	)
	reg.Open("u/1", 80, 24)
	if !closed {
		t.Fatal("an instant dial failure must surface as an exit")
	}
	// The tile holds nothing afterwards.
	reg.Write("u/1", []byte{1})
}

// The initial size rides the dial, which is what binds it.
func TestOpenCarriesTheInitialSize(t *testing.T) {
	h := newHarness()
	h.reg.Open("u/1", 132, 43)
	if h.dialed[0].cols != 132 || h.dialed[0].rows != 43 {
		t.Fatalf("dial got %dx%d, want 132x43", h.dialed[0].cols, h.dialed[0].rows)
	}
}

// Output and the end name the tile, never a pane: whichever pane shows the
// terminal now receives them, so a takeover moves it without a redial.
func TestOutputAndExitNameTheTile(t *testing.T) {
	h := newHarness()
	h.reg.Open("u/7", 80, 24)
	h.dialed[0].onData([]byte{1})
	h.dialed[0].onEnd("boom", false)
	if len(h.data) != 1 || h.data[0].tileID != "u/7" {
		t.Fatalf("data = %+v, want one chunk for u/7", h.data)
	}
	if len(h.exits) != 1 || h.exits[0].TileID != "u/7" {
		t.Fatalf("exits = %+v, want one for u/7", h.exits)
	}
}
