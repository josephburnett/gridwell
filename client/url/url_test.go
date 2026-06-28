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
	got := Encode(State{TileIDs: []string{"3", "4", "5"}})
	if got != "/3/4/5" {
		t.Errorf("got %q", got)
	}
}

func TestEncodeFileText(t *testing.T) {
	got := Encode(State{TileIDs: []string{"3", "4", "5", "9"}, CursorMode: true, Col: 24, Row: 10})
	want := "/3/4/5/9?c=24&r=10"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEncodeFileTextAtOrigin(t *testing.T) {
	// Cursor at (0, 0) is still emitted: presence implies text mode.
	got := Encode(State{TileIDs: []string{"9"}, CursorMode: true})
	want := "/9?c=0&r=0"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEncodeOmitsDefaultZoom(t *testing.T) {
	if got := Encode(State{TileIDs: []string{"1"}, Zoom: 1.0}); got != "/1" {
		t.Errorf("got %q", got)
	}
}

func TestEncodeOmitsZeroXY(t *testing.T) {
	got := Encode(State{TileIDs: []string{"1"}, Zoom: 1.5})
	if got != "/1?z=1.5" {
		t.Errorf("got %q", got)
	}
}

func TestEncodeStripsTrailingZeros(t *testing.T) {
	got := Encode(State{TileIDs: []string{"1"}, X: 0.5, Y: 1.0, Zoom: 2.0})
	// X=0.5 → "0.5"; Y=1.0 → "1"; Zoom=2.0 → "2"
	want := "/1?x=0.5&y=1&z=2"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Encode strips plugin UUID prefix so URLs stay readable.
func TestEncodeStripsUUIDPrefix(t *testing.T) {
	got := Encode(State{TileIDs: []string{"abc-uuid/3", "abc-uuid/4"}})
	if got != "/3/4" {
		t.Errorf("got %q, want /3/4", got)
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
	if !reflect.DeepEqual(s.TileIDs, []string{"3", "4", "5"}) {
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
	if !reflect.DeepEqual(s.TileIDs, []string{"3", "4"}) {
		t.Errorf("TileIDs = %v", s.TileIDs)
	}
}

func TestRoundTrip(t *testing.T) {
	cases := []State{
		{},
		{TileIDs: []string{"3", "4", "5"}, X: 12.5, Y: -3.25, Zoom: 1.5},
		{TileIDs: []string{"9"}, CursorMode: true, Col: 0, Row: 0},
		{TileIDs: []string{"42", "100", "99"}, CursorMode: true, Col: 100, Row: 25},
		{TileIDs: []string{"7"}, Zoom: 1.234},
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

func TestDecodeMalformed(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"no leading slash", "3/4/5", true},
		{"non-numeric segment", "/3/foo/5", true},
		{"int64 overflow", "/99999999999999999999999999", true},
		{"bad query escape", "/3?x=%zz", true},
		{"empty middle segment tolerated", "/3//5", false},
		{"trailing slash tolerated", "/3/4/", false},
		{"negative id ok", "/-2/3", false},
	}
	for _, c := range cases {
		_, err := Decode(c.raw)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: Decode(%q) err=%v wantErr=%v", c.name, c.raw, err, c.wantErr)
		}
	}
}

func TestDecodeEmptyMiddleSegment(t *testing.T) {
	s, err := Decode("/3//5")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.TileIDs, []string{"3", "5"}) {
		t.Errorf("TileIDs = %v, want [3 5]", s.TileIDs)
	}
}

func TestDecodeNegativeID(t *testing.T) {
	s, err := Decode("/-2/3")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.TileIDs, []string{"-2", "3"}) {
		t.Errorf("TileIDs = %v, want [-2 3]", s.TileIDs)
	}
}

// Bad float values in x/y/z are silently ignored (ParseFloat error ->
// the field stays 0), and Decode returns no error. Lock that contract.
func TestDecodeBadFloatIgnored(t *testing.T) {
	s, err := Decode("/3?x=notnum&y=2&z=bad")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if s.X != 0 {
		t.Errorf("X = %v, want 0 (bad float ignored)", s.X)
	}
	if s.Y != 2 {
		t.Errorf("Y = %v, want 2", s.Y)
	}
	if s.Zoom != 0 {
		t.Errorf("Zoom = %v, want 0 (bad float ignored)", s.Zoom)
	}
}

// `c` present without `r` is not enough for cursor mode, and because the
// c-branch is taken, x/y/z are NOT read either. Lock this exact shape.
func TestDecodeCursorRequiresBothCAndR(t *testing.T) {
	s, err := Decode("/9?c=5&x=12")
	if err != nil {
		t.Fatal(err)
	}
	if s.CursorMode {
		t.Error("CursorMode should be false when r is absent")
	}
	if s.X != 0 {
		t.Errorf("X = %v, want 0 (c-branch suppresses viewport read)", s.X)
	}
}

// Non-numeric c/r leaves CursorMode false (Atoi error path).
func TestDecodeBadCursorIgnored(t *testing.T) {
	s, err := Decode("/9?c=foo&r=bar")
	if err != nil {
		t.Fatal(err)
	}
	if s.CursorMode {
		t.Error("CursorMode should be false for non-numeric c/r")
	}
}

func TestTextStateRendered(t *testing.T) {
	// Rendered text leaf: no cursor encoded.
	s := TextState([]string{"3", "4"}, "9", false, 12, 7)
	if !reflect.DeepEqual(s.TileIDs, []string{"3", "4", "9"}) {
		t.Errorf("TileIDs = %v, want [3 4 9]", s.TileIDs)
	}
	if s.CursorMode {
		t.Error("rendered leaf should not set CursorMode")
	}
	if s.Col != 0 || s.Row != 0 {
		t.Errorf("cursor leaked: col=%d row=%d", s.Col, s.Row)
	}
}

func TestTextStateTextMode(t *testing.T) {
	s := TextState([]string{"3"}, "9", true, 12, 7)
	if !reflect.DeepEqual(s.TileIDs, []string{"3", "9"}) {
		t.Errorf("TileIDs = %v", s.TileIDs)
	}
	if !s.CursorMode || s.Col != 12 || s.Row != 7 {
		t.Errorf("cursor = (mode=%v, c=%d, r=%d)", s.CursorMode, s.Col, s.Row)
	}
}

func TestTextStateClonesPath(t *testing.T) {
	path := []string{"3", "4"}
	s := TextState(path, "9", false, 0, 0)
	s.TileIDs[0] = "99" // mutating the result must not touch the caller's slice
	if path[0] != "3" {
		t.Errorf("TextState retained/aliased caller path: %v", path)
	}
}

func TestGridState(t *testing.T) {
	s := GridState([]string{"3", "4", "5"}, 12.5, -3, 1.5)
	if !reflect.DeepEqual(s.TileIDs, []string{"3", "4", "5"}) {
		t.Errorf("TileIDs = %v", s.TileIDs)
	}
	if s.X != 12.5 || s.Y != -3 || s.Zoom != 1.5 {
		t.Errorf("viewport = (%v,%v,%v)", s.X, s.Y, s.Zoom)
	}
	if s.CursorMode {
		t.Error("grid state should not set CursorMode")
	}
}

func TestGridStateClonesPath(t *testing.T) {
	path := []string{"7"}
	s := GridState(path, 0, 0, 1)
	s.TileIDs[0] = "99"
	if path[0] != "7" {
		t.Errorf("GridState retained/aliased caller path: %v", path)
	}
}

func TestBootViewport(t *testing.T) {
	cases := []struct {
		name       string
		ux, uy, uz float64
		rx, ry, rz float64
		want       BootView
	}{
		{"url viewport with zoom wins", 5, -3, 1.5, 9, 9, 2,
			BootView{Apply: true, Cx: 5, Cy: -3, SetZoom: true, Zoom: 1.5}},
		{"url pan only (no zoom) keeps pane zoom", 5, -3, 0, 9, 9, 2,
			BootView{Apply: true, Cx: 5, Cy: -3, SetZoom: false}},
		{"url zoom only (x,y zero) still applies", 0, 0, 1.5, 9, 9, 2,
			BootView{Apply: true, Cx: 0, Cy: 0, SetZoom: true, Zoom: 1.5}},
		{"no url -> stored root view", 0, 0, 0, 9, 8, 2,
			BootView{Apply: true, Cx: 9, Cy: 8, SetZoom: true, Zoom: 2}},
		{"no url, no stored zoom -> nothing", 0, 0, 0, 9, 8, 0,
			BootView{}},
		{"url negative zoom ignored as zoom, but x/y still apply", 4, 0, -1, 9, 8, 2,
			BootView{Apply: true, Cx: 4, Cy: 0, SetZoom: false}},
	}
	for _, c := range cases {
		if got := BootViewport(c.ux, c.uy, c.uz, c.rx, c.ry, c.rz); got != c.want {
			t.Errorf("%s: BootViewport = %+v, want %+v", c.name, got, c.want)
		}
	}
}

// TestEncodeAnchor: the anchor (the plugin root the pane sits inside) rides in
// the `a=` query param alongside the descent path. A bare descent path keeps
// its short, readable form; the anchor is the qualified grid id.
func TestEncodeAnchor(t *testing.T) {
	got := Encode(State{Anchor: "fs-uuid/1", TileIDs: []string{"3", "4"}})
	want := "/3/4?a=fs-uuid%2F1"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestEncodeOmitsEmptyAnchor: the launcher start screen has no anchor, so no
// `a=` param is emitted.
func TestEncodeOmitsEmptyAnchor(t *testing.T) {
	if got := Encode(State{}); got != "/" {
		t.Errorf("got %q, want /", got)
	}
}

// TestAnchorRoundTrip: Encode then Decode preserves the anchor and path.
func TestAnchorRoundTrip(t *testing.T) {
	in := State{Anchor: "db-uuid/9", TileIDs: []string{"12", "7"}}
	out, err := Decode(Encode(in))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if out.Anchor != in.Anchor {
		t.Errorf("anchor = %q, want %q", out.Anchor, in.Anchor)
	}
	if !reflect.DeepEqual(out.TileIDs, in.TileIDs) {
		t.Errorf("tile ids = %v, want %v", out.TileIDs, in.TileIDs)
	}
}
