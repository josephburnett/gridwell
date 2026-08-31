package pane

import (
	"reflect"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

func TestEncodeRoot(t *testing.T) {
	if got := EncodeURL(URLState{}); got != "/" {
		t.Errorf("empty state = %q, want /", got)
	}
}

func TestEncodeRootWithViewport(t *testing.T) {
	got := EncodeURL(URLState{X: 5.5, Y: -2, Zoom: 1.5})
	want := "/?x=5.5&y=-2&z=1.5"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEncodePath(t *testing.T) {
	got := EncodeURL(URLState{TileIDs: []string{"3", "4", "5"}})
	if got != "/3/4/5" {
		t.Errorf("got %q", got)
	}
}

func TestEncodeFileText(t *testing.T) {
	got := EncodeURL(URLState{TileIDs: []string{"3", "4", "5", "9"}, CursorMode: true, Col: 24, Row: 10})
	want := "/3/4/5/9?c=24&r=10"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEncodeFileTextAtOrigin(t *testing.T) {
	// Cursor at (0, 0) is still emitted: presence implies text mode.
	got := EncodeURL(URLState{TileIDs: []string{"9"}, CursorMode: true})
	want := "/9?c=0&r=0"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestEncodeOmitsDefaultZoom(t *testing.T) {
	if got := EncodeURL(URLState{TileIDs: []string{"1"}, Zoom: 1.0}); got != "/1" {
		t.Errorf("got %q", got)
	}
}

func TestEncodeOmitsZeroXY(t *testing.T) {
	got := EncodeURL(URLState{TileIDs: []string{"1"}, Zoom: 1.5})
	if got != "/1?z=1.5" {
		t.Errorf("got %q", got)
	}
}

func TestEncodeStripsTrailingZeros(t *testing.T) {
	got := EncodeURL(URLState{TileIDs: []string{"1"}, X: 0.5, Y: 1.0, Zoom: 2.0})
	// X=0.5 → "0.5"; Y=1.0 → "1"; Zoom=2.0 → "2"
	want := "/1?x=0.5&y=1&z=2"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// EncodeURL strips plugin UUID prefix so URLs stay readable.
func TestEncodeStripsUUIDPrefix(t *testing.T) {
	got := EncodeURL(URLState{TileIDs: []string{"abc-uuid/3", "abc-uuid/4"}})
	if got != "/3/4" {
		t.Errorf("got %q, want /3/4", got)
	}
}

func TestDecodeRoot(t *testing.T) {
	for _, in := range []string{"", "/"} {
		s, err := DecodeURL(in)
		if err != nil {
			t.Fatalf("DecodeURL(%q) err: %v", in, err)
		}
		if len(s.TileIDs) != 0 {
			t.Errorf("DecodeURL(%q) TileIDs = %v, want empty", in, s.TileIDs)
		}
	}
}

func TestDecodePath(t *testing.T) {
	s, err := DecodeURL("/3/4/5")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.TileIDs, []string{"3", "4", "5"}) {
		t.Errorf("TileIDs = %v", s.TileIDs)
	}
}

func TestDecodeWithViewport(t *testing.T) {
	s, err := DecodeURL("/3?x=5.5&y=-2&z=1.5")
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
	s, err := DecodeURL("/9?c=24&r=10")
	if err != nil {
		t.Fatal(err)
	}
	if !s.CursorMode || s.Col != 24 || s.Row != 10 {
		t.Errorf("cursor = (mode=%v, c=%d, r=%d)", s.CursorMode, s.Col, s.Row)
	}
}

func TestDecodeRejectsNonNumericSegments(t *testing.T) {
	// "/foo" can't be a tile-id path — non-numeric segment.
	if _, err := DecodeURL("/foo"); err == nil {
		t.Error("expected error for /foo")
	}
}

func TestDecodeIgnoresTrailingSlash(t *testing.T) {
	s, err := DecodeURL("/3/4/")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.TileIDs, []string{"3", "4"}) {
		t.Errorf("TileIDs = %v", s.TileIDs)
	}
}

func TestRoundTrip(t *testing.T) {
	cases := []URLState{
		{},
		{TileIDs: []string{"3", "4", "5"}, X: 12.5, Y: -3.25, Zoom: 1.5},
		{TileIDs: []string{"9"}, CursorMode: true, Col: 0, Row: 0},
		{TileIDs: []string{"42", "100", "99"}, CursorMode: true, Col: 100, Row: 25},
		{TileIDs: []string{"7"}, Zoom: 1.234},
	}
	for _, in := range cases {
		raw := EncodeURL(in)
		got, err := DecodeURL(raw)
		if err != nil {
			t.Fatalf("DecodeURL(%q) err: %v", raw, err)
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
		_, err := DecodeURL(c.raw)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: DecodeURL(%q) err=%v wantErr=%v", c.name, c.raw, err, c.wantErr)
		}
	}
}

func TestDecodeEmptyMiddleSegment(t *testing.T) {
	s, err := DecodeURL("/3//5")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.TileIDs, []string{"3", "5"}) {
		t.Errorf("TileIDs = %v, want [3 5]", s.TileIDs)
	}
}

func TestDecodeNegativeID(t *testing.T) {
	s, err := DecodeURL("/-2/3")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.TileIDs, []string{"-2", "3"}) {
		t.Errorf("TileIDs = %v, want [-2 3]", s.TileIDs)
	}
}

// Bad float values in x/y/z are silently ignored (ParseFloat error ->
// the field stays 0), and DecodeURL returns no error. Lock that contract.
func TestDecodeBadFloatIgnored(t *testing.T) {
	s, err := DecodeURL("/3?x=notnum&y=2&z=bad")
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
// c-branch is taken, x, y, and z are not read either. Lock this exact
// shape.
func TestDecodeCursorRequiresBothCAndR(t *testing.T) {
	s, err := DecodeURL("/9?c=5&x=12")
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
	s, err := DecodeURL("/9?c=foo&r=bar")
	if err != nil {
		t.Fatal(err)
	}
	if s.CursorMode {
		t.Error("CursorMode should be false for non-numeric c/r")
	}
}

// URLStateOf is the one encode half: the pane's frame stack projected into
// the URL DTO. A grid place carries its viewport; a content place carries the
// tile id and, in raw-text mode, the cursor.
func TestURLStateOfGridPlace(t *testing.T) {
	p := &Pane{ID: "p1", Stack: StackAt("u1/1", []string{"3", "4", "5"}, "")}
	p.Cx, p.Cy, p.Zoom = 12.5, -3, 1.5
	s := URLStateOf(&p.Stack, "home/1", false, 0, 0)
	if !reflect.DeepEqual(s.TileIDs, []string{"3", "4", "5"}) {
		t.Errorf("TileIDs = %v", s.TileIDs)
	}
	if s.Anchor != "u1/1" {
		t.Errorf("Anchor = %q", s.Anchor)
	}
	if s.X != 12.5 || s.Y != -3 || s.Zoom != 1.5 {
		t.Errorf("viewport = (%v,%v,%v)", s.X, s.Y, s.Zoom)
	}
	if s.CursorMode {
		t.Error("grid place should not set CursorMode")
	}
}

// Home encodes as an empty anchor, so "/" stays home's URL.
func TestURLStateOfHomeAnchorIsEmpty(t *testing.T) {
	p := &Pane{ID: "p1", Stack: StackAt("home/1", nil, "")}
	if s := URLStateOf(&p.Stack, "home/1", false, 0, 0); s.Anchor != "" {
		t.Errorf("home anchor = %q, want empty", s.Anchor)
	}
}

func TestURLStateOfContentPlace(t *testing.T) {
	p := &Pane{ID: "p1", Stack: StackAt("home/1", []string{"3", "4"}, "9")}
	s := URLStateOf(&p.Stack, "home/1", false, 12, 7)
	if !reflect.DeepEqual(s.TileIDs, []string{"3", "4", "9"}) {
		t.Errorf("TileIDs = %v, want [3 4 9]", s.TileIDs)
	}
	if s.CursorMode || s.Col != 0 || s.Row != 0 {
		t.Errorf("rendered leaf leaked a cursor: %+v", s)
	}
	s = URLStateOf(&p.Stack, "home/1", true, 12, 7)
	if !s.CursorMode || s.Col != 12 || s.Row != 7 {
		t.Errorf("cursor = (mode=%v, c=%d, r=%d)", s.CursorMode, s.Col, s.Row)
	}
	if s.X != 0 || s.Zoom != 0 {
		t.Errorf("a content place carries no grid viewport: %+v", s)
	}
}

// The URL is an ENCODING of the stack, not a second model: encode → decode
// → StackAt round-trips the place (the id walk the client does against its
// cache is the only step this test stands in for).
func TestURLRoundTripsThePlace(t *testing.T) {
	p := &Pane{ID: "p1", Stack: StackAt("u1/1", []string{"3", "4"}, "9")}
	raw := EncodeURL(URLStateOf(&p.Stack, "home/1", true, 2, 3))
	st, err := DecodeURL(raw)
	if err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	back := StackAt(st.Anchor, st.TileIDs[:len(st.TileIDs)-1], st.TileIDs[len(st.TileIDs)-1])
	if back.Anchor() != p.Anchor() || !reflect.DeepEqual(back.Path(), p.Path()) ||
		back.ContentID() != p.ContentID() || back.Depth() != p.Depth() {
		t.Fatalf("round trip: %+v -> %+v", p.Crumbs(), back.Crumbs())
	}
}

func TestBootViewport(t *testing.T) {
	cases := []struct {
		name       string
		ux, uy, uz float64
		rx, ry, rz float64
		want       URLBootView
	}{
		{"url viewport with zoom wins", 5, -3, 1.5, 9, 9, 2,
			URLBootView{Apply: true, Cx: 5, Cy: -3, SetZoom: true, Zoom: 1.5}},
		{"url pan only (no zoom) keeps pane zoom", 5, -3, 0, 9, 9, 2,
			URLBootView{Apply: true, Cx: 5, Cy: -3, SetZoom: false}},
		{"url zoom only (x,y zero) still applies", 0, 0, 1.5, 9, 9, 2,
			URLBootView{Apply: true, Cx: 0, Cy: 0, SetZoom: true, Zoom: 1.5}},
		{"no url -> stored root view", 0, 0, 0, 9, 8, 2,
			URLBootView{Apply: true, Cx: 9, Cy: 8, SetZoom: true, Zoom: 2}},
		{"no url, no stored zoom -> nothing", 0, 0, 0, 9, 8, 0,
			URLBootView{}},
		{"url negative zoom ignored as zoom, but x/y still apply", 4, 0, -1, 9, 8, 2,
			URLBootView{Apply: true, Cx: 4, Cy: 0, SetZoom: false}},
	}
	for _, c := range cases {
		if got := URLBootViewport(c.ux, c.uy, c.uz, c.rx, c.ry, c.rz); got != c.want {
			t.Errorf("%s: URLBootViewport = %+v, want %+v", c.name, got, c.want)
		}
	}
}

// TestEncodeAnchorAsPath: the anchor is leading path segments — a plugin id
// is just another part of the path. The anchor is already a slash-joined
// qualified grid id, so it drops straight in, and tile ids follow.
func TestEncodeAnchorAsPath(t *testing.T) {
	cases := []struct {
		in   URLState
		want string
	}{
		{URLState{Anchor: "k3x9m2q/1", TileIDs: []string{"3", "4"}}, "/k3x9m2q/1/3/4"},
		{URLState{Anchor: "k3x9m2q/1"}, "/k3x9m2q/1"},
		// Chained remote anchor: each hop is one more leading segment.
		{URLState{Anchor: "ssh4321/remote9/1", TileIDs: []string{"4", "7"}}, "/ssh4321/remote9/1/4/7"},
		// Node grid: grid id 0 is a valid anchor grid segment.
		{URLState{Anchor: "abc1234/0"}, "/abc1234/0"},
		// A 32-hex id encodes the same way.
		{URLState{Anchor: "0123456789abcdef0123456789abcdef/1", TileIDs: []string{"5"}},
			"/0123456789abcdef0123456789abcdef/1/5"},
		// Viewport rides in the query as before.
		{URLState{Anchor: "k3x9m2q/1", TileIDs: []string{"3"}, X: 5.5, Zoom: 1.5}, "/k3x9m2q/1/3?x=5.5&z=1.5"},
	}
	for _, c := range cases {
		if got := EncodeURL(c.in); got != c.want {
			t.Errorf("EncodeURL(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestEncodeOmitsEmptyAnchor: home has no anchor segments — "/" is home.
func TestEncodeOmitsEmptyAnchor(t *testing.T) {
	if got := EncodeURL(URLState{}); got != "/" {
		t.Errorf("got %q, want /", got)
	}
}

// TestAnchorRoundTrip: EncodeURL then DecodeURL preserves the anchor and path, for
// single and chained namespaces, both id shapes.
func TestAnchorRoundTrip(t *testing.T) {
	cases := []URLState{
		{Anchor: "k3x9m2q/9", TileIDs: []string{"12", "7"}},
		{Anchor: "k3x9m2q/9"},
		{Anchor: "ssh4321/remote9/1", TileIDs: []string{"4"}},
		{Anchor: "abc1234/0"},
		{Anchor: "0123456789abcdef0123456789abcdef/1", TileIDs: []string{"5"}},
	}
	for _, in := range cases {
		out, err := DecodeURL(EncodeURL(in))
		if err != nil {
			t.Fatalf("DecodeURL(EncodeURL(%+v)): %v", in, err)
		}
		if out.Anchor != in.Anchor {
			t.Errorf("anchor = %q, want %q", out.Anchor, in.Anchor)
		}
		if !reflect.DeepEqual(out.TileIDs, in.TileIDs) {
			t.Errorf("tile ids = %v, want %v", out.TileIDs, in.TileIDs)
		}
	}
}

// TestDecodeLegacyAnchorQuery: the `?a=` form still decodes, so old bookmarks
// keep resolving, but the path form wins when both are present and EncodeURL
// never emits `a=`.
func TestDecodeLegacyAnchorQuery(t *testing.T) {
	s, err := DecodeURL("/3/4?a=fs-uuid%2F1")
	if err != nil {
		t.Fatal(err)
	}
	if s.Anchor != "fs-uuid/1" {
		t.Errorf("legacy anchor = %q, want fs-uuid/1", s.Anchor)
	}
	if !reflect.DeepEqual(s.TileIDs, []string{"3", "4"}) {
		t.Errorf("tile ids = %v", s.TileIDs)
	}
	// The path anchor beats a conflicting query anchor.
	s2, err := DecodeURL("/k3x9m2q/1/3?a=other%2F9")
	if err != nil {
		t.Fatal(err)
	}
	if s2.Anchor != "k3x9m2q/1" {
		t.Errorf("path anchor should win: %q", s2.Anchor)
	}
	// Re-encoding a query-anchor decode yields the path form.
	if got := EncodeURL(s); got != "/fs-uuid/1/3/4" {
		t.Errorf("re-encode of legacy = %q, want /fs-uuid/1/3/4", got)
	}
}

// TestDecodeAnchorGrammarRejects: namespace segments with no grid id, and
// non-numeric segments after the grid, are not Gridwell places.
func TestDecodeAnchorGrammarRejects(t *testing.T) {
	for _, raw := range []string{
		"/k3x9m2q",       // namespace, no grid
		"/foo/bar",       // two namespace segments, no grid
		"/k3x9m2q/1/3/x", // non-numeric after the grid
		"/k3x9m2q/1/x/3", // non-numeric mid-descent
		"/a/b/c",         // namespace chain, never a grid
	} {
		if _, err := DecodeURL(raw); err == nil {
			t.Errorf("DecodeURL(%q) accepted; want error", raw)
		}
	}
}

// TestPushesEntry pins the one owner of "does this navigation deserve a
// browser history entry": structural moves push, framing and focus switches
// replace.
func TestPushesEntry(t *testing.T) {
	at := func(pane, ws, anchor, path string) URLPlace {
		return URLPlace{PaneID: pane, Workspace: ws, Anchor: anchor, Path: path}
	}
	cases := []struct {
		name       string
		prev, next URLPlace
		seen       bool
		want       bool
	}{
		{"first write after boot replaces", at("p1", "", "", ""), at("p1", "", "k/1", "3"), false, false},
		{"framing-only change replaces", at("p1", "", "k/1", "3"), at("p1", "", "k/1", "3"), true, false},
		{"descend pushes", at("p1", "", "k/1", ""), at("p1", "", "k/1", "3"), true, true},
		{"ascend pushes", at("p1", "", "k/1", "3/4"), at("p1", "", "k/1", "3"), true, true},
		{"portal (anchor swap) pushes", at("p1", "", "k/1", "3"), at("p1", "", "m/9", ""), true, true},
		{"text descent pushes", at("p1", "", "k/1", "3"), at("p1", "", "k/1", "3/9"), true, true},
		{"focus switch replaces", at("p1", "", "k/1", "3"), at("p2", "", "k/1", ""), true, false},
		{"workspace enter pushes despite pane swap", at("p1", "", "k/1", ""), at("w-p1", "k/42", "", ""), true, true},
		{"workspace exit pushes despite pane swap", at("w-p1", "k/42", "", ""), at("p1", "", "k/1", ""), true, true},
		{"inside a workspace the URL is constant — replaces", at("w-p1", "k/42", "", ""), at("w-p2", "k/42", "", ""), true, false},
	}
	for _, c := range cases {
		if got := URLPushesEntry(c.prev, c.next, c.seen); got != c.want {
			t.Errorf("%s: URLPushesEntry = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestWorkspaceURLRoundTrip: inside a pane tile, that tile is the whole place
// — `?w=<qualified tile id>` and nothing else. The interior is server-owned
// by the layout blob, so encoding a path or viewport alongside it would be a
// second copy of a fact the blob owns.
func TestWorkspaceURLRoundTrip(t *testing.T) {
	raw := EncodeURL(URLState{Workspace: "abcd-uuid/42", Anchor: "ignored/1", TileIDs: []string{"7"}, X: 3})
	if raw != "/?w=abcd-uuid%2F42" {
		t.Fatalf("EncodeURL workspace = %q", raw)
	}
	s, err := DecodeURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	if s.Workspace != "abcd-uuid/42" {
		t.Fatalf("Workspace = %q", s.Workspace)
	}
	if s.Anchor != "" || len(s.TileIDs) != 0 {
		t.Fatalf("workspace URL must carry nothing else: %+v", s)
	}
	// A chained (remote) pane-tile id survives the round trip.
	s2, err := DecodeURL(EncodeURL(URLState{Workspace: "ssh-uuid/plugin-uuid/9"}))
	if err != nil || s2.Workspace != "ssh-uuid/plugin-uuid/9" {
		t.Fatalf("chained workspace: %+v err=%v", s2, err)
	}
}

// The key-form tile segment rides the URL like a row id: a leaf content
// descent, a well mid-path, and a slashy key alike. The grammar is pinned to
// itself — whatever EncodeURL writes, DecodeURL reads back — because the two
// halves are the only place the shape is spelled, and a URL that decodes to a
// different place than it encoded silently relocates the user on reload.
func TestKeyFormURLRoundTrip(t *testing.T) {
	deep := rpc.KeyTileID("/home/joe/notes/a b.md")
	for _, st := range []URLState{
		// A key-form content leaf under a plugin anchor.
		{Anchor: "k3x9m2q/1", TileIDs: []string{"3", rpc.KeyTileID("/home/joe/x")}},
		// A key-form WELL mid-path, descending on into a row id.
		{Anchor: "k3x9m2q/1", TileIDs: []string{rpc.KeyTileID("/home/joe"), "4"}},
		// A key whose bytes are full of slashes stays exactly one segment.
		{Anchor: "ssh4321/remote9/1", TileIDs: []string{deep}},
		// Under the home anchor, with no namespace prefix at all.
		{TileIDs: []string{rpc.KeyTileID("/etc")}},
		// A text-mode content leaf, cursor and all.
		{Anchor: "k3x9m2q/1", TileIDs: []string{deep}, CursorMode: true, Col: 24, Row: 10},
		// A grid leaf with a viewport, reached through a key-form well.
		{Anchor: "k3x9m2q/1", TileIDs: []string{deep, "7"}, X: 5.5, Y: -2, Zoom: 1.5},
	} {
		raw := EncodeURL(st)
		got, err := DecodeURL(raw)
		if err != nil {
			t.Fatalf("DecodeURL(%q) from %+v: %v", raw, st, err)
		}
		if !reflect.DeepEqual(got, st) {
			t.Fatalf("round trip through %q: got %+v, want %+v", raw, got, st)
		}
	}
}

// A key form is a TILE segment, never a namespace hop: the anchor boundary
// stops at it. "/~<key>" is a tile in the home grid, and "/k3x9m2q/~<key>"
// names the plugin's key-form grid, not a two-hop namespace chain.
func TestKeyFormIsNeverANamespaceSegment(t *testing.T) {
	key := rpc.KeyTileID("/home/joe/x")
	got, err := DecodeURL("/" + key)
	if err != nil {
		t.Fatalf("DecodeURL(/%s): %v", key, err)
	}
	if got.Anchor != "" || !reflect.DeepEqual(got.TileIDs, []string{key}) {
		t.Fatalf("leading key form read as a namespace: %+v", got)
	}
	got, err = DecodeURL("/k3x9m2q/" + key + "/4")
	if err != nil {
		t.Fatalf("DecodeURL: %v", err)
	}
	if got.Anchor != "k3x9m2q/"+key || !reflect.DeepEqual(got.TileIDs, []string{"4"}) {
		t.Fatalf("key-form anchor grid misread: %+v", got)
	}
}

// A '~' segment that is not canonical base64url is not a key form, so it is
// rejected mid-descent exactly as any other non-tile segment is. The URL
// grammar inherits that from rpc.ShapeOf rather than deciding it again.
func TestDecodeRejectsMalformedKeyForm(t *testing.T) {
	for _, raw := range []string{
		"/k3x9m2q/1/~AC",         // non-canonical trailing bits
		"/k3x9m2q/1/~AB==",       // padded
		"/k3x9m2q/1/~not base64", // not the alphabet
	} {
		if _, err := DecodeURL(raw); err == nil {
			t.Errorf("DecodeURL(%q) accepted; want error", raw)
		}
	}
}

// EncodeURL writes a tile id's LAST segment, whatever its shape: a qualified
// key-form id strips to the key form, never to a fragment of it.
func TestEncodeStripsNamespaceFromKeyForm(t *testing.T) {
	key := rpc.KeyTileID("/home/joe/x")
	got := EncodeURL(URLState{Anchor: "k3x9m2q/1", TileIDs: []string{rpc.QualifyID("k3x9m2q", key)}})
	if want := "/k3x9m2q/1/" + key; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
