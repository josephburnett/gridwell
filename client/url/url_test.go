package url

import (
	"reflect"
	"testing"
)

func TestEncodeRoot(t *testing.T) {
	if got := Encode(State{}); got != "/" {
		t.Errorf("empty state = %q, want /", got)
	}
}

func TestEncodeRootWithViewport(t *testing.T) {
	got := Encode(State{X: 5.5, Y: -2, Zoom: 1.5})
	want := "/?x=5.5&y=-2&z=1.5"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEncodePath(t *testing.T) {
	got := Encode(State{TileIDs: []int64{3, 4, 5}})
	if got != "/3/4/5" {
		t.Errorf("got %q", got)
	}
}

func TestEncodeFileText(t *testing.T) {
	got := Encode(State{TileIDs: []int64{3, 4, 5, 9}, CursorMode: true, Col: 24, Row: 10})
	want := "/3/4/5/9?c=24&r=10"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEncodeFileTextAtOrigin(t *testing.T) {
	// Cursor at (0, 0) is still emitted: presence implies text mode.
	got := Encode(State{TileIDs: []int64{9}, CursorMode: true})
	want := "/9?c=0&r=0"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEncodeOmitsDefaultZoom(t *testing.T) {
	if got := Encode(State{TileIDs: []int64{1}, Zoom: 1.0}); got != "/1" {
		t.Errorf("got %q", got)
	}
}

func TestEncodeOmitsZeroXY(t *testing.T) {
	got := Encode(State{TileIDs: []int64{1}, Zoom: 1.5})
	if got != "/1?z=1.5" {
		t.Errorf("got %q", got)
	}
}

func TestEncodeStripsTrailingZeros(t *testing.T) {
	got := Encode(State{TileIDs: []int64{1}, X: 0.5, Y: 1.0, Zoom: 2.0})
	// X=0.5 → "0.5"; Y=1.0 → "1"; Zoom=2.0 → "2"
	want := "/1?x=0.5&y=1&z=2"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDecodeRoot(t *testing.T) {
	for _, in := range []string{"", "/"} {
		s, err := Decode(in)
		if err != nil {
			t.Fatalf("Decode(%q) err: %v", in, err)
		}
		if len(s.TileIDs) != 0 {
			t.Errorf("Decode(%q) TileIDs = %v, want empty", in, s.TileIDs)
		}
	}
}

func TestDecodePath(t *testing.T) {
	s, err := Decode("/3/4/5")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.TileIDs, []int64{3, 4, 5}) {
		t.Errorf("TileIDs = %v", s.TileIDs)
	}
}

func TestDecodeWithViewport(t *testing.T) {
	s, err := Decode("/3?x=5.5&y=-2&z=1.5")
	if err != nil {
		t.Fatal(err)
	}
	if s.X != 5.5 || s.Y != -2 || s.Zoom != 1.5 {
		t.Errorf("viewport = (%v, %v, %v)", s.X, s.Y, s.Zoom)
	}
	if s.CursorMode {
		t.Error("expected !CursorMode")
	}
}

func TestDecodeWithCursor(t *testing.T) {
	s, err := Decode("/9?c=24&r=10")
	if err != nil {
		t.Fatal(err)
	}
	if !s.CursorMode || s.Col != 24 || s.Row != 10 {
		t.Errorf("cursor = (mode=%v, c=%d, r=%d)", s.CursorMode, s.Col, s.Row)
	}
}

func TestDecodeRejectsNonNumericSegments(t *testing.T) {
	// "/foo" can't be a tile-id path — non-numeric segment.
	if _, err := Decode("/foo"); err == nil {
		t.Error("expected error for /foo")
	}
}

func TestDecodeIgnoresTrailingSlash(t *testing.T) {
	s, err := Decode("/3/4/")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.TileIDs, []int64{3, 4}) {
		t.Errorf("TileIDs = %v", s.TileIDs)
	}
}

func TestRoundTrip(t *testing.T) {
	cases := []State{
		{},
		{TileIDs: []int64{3, 4, 5}, X: 12.5, Y: -3.25, Zoom: 1.5},
		{TileIDs: []int64{9}, CursorMode: true, Col: 0, Row: 0},
		{TileIDs: []int64{42, 100, 99}, CursorMode: true, Col: 100, Row: 25},
		{TileIDs: []int64{7}, Zoom: 1.234},
	}
	for _, in := range cases {
		raw := Encode(in)
		got, err := Decode(raw)
		if err != nil {
			t.Fatalf("Decode(%q) err: %v", raw, err)
		}
		if !reflect.DeepEqual(got, in) {
			t.Errorf("round trip: in=%+v out=%+v (raw=%q)", in, got, raw)
		}
	}
}
