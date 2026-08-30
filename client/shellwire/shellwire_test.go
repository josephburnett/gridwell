package shellwire

import (
	"net/url"
	"testing"
)

// The grammar is pinned to itself: what AttachURL writes is exactly what
// ParseAttach reads. A round trip is the only test that catches one side
// drifting. The content URL grammar (rpc.PageURL) is pinned the same way.
func TestAttachURLRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		origin string
		scheme string
	}{
		{"http://127.0.0.1:8099", "ws"},
		{"https://box.tailnet.ts.net", "wss"},
		{"http://[::1]:9000", "ws"},
	} {
		raw, err := AttachURL(tc.origin, "abc1234/12", 120, 40)
		if err != nil {
			t.Fatalf("AttachURL(%q): %v", tc.origin, err)
		}
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		if u.Scheme != tc.scheme {
			t.Fatalf("%q: scheme %q, want %q", raw, u.Scheme, tc.scheme)
		}
		if u.Path != Path {
			t.Fatalf("%q: path %q, want %q", raw, u.Path, Path)
		}
		a, err := ParseAttach(u.Query())
		if err != nil {
			t.Fatalf("ParseAttach(%q): %v", raw, err)
		}
		if a.TileID != "abc1234/12" || a.Cols != 120 || a.Rows != 40 {
			t.Fatalf("%q: round trip gave %+v", raw, a)
		}
	}
}

// A qualified id is a chain with slashes; it survives the query string
// intact, or a mounted remote's shell attaches to the wrong tile.
func TestAttachURLQualifiedChain(t *testing.T) {
	const id = "abc1234/desk/xyz9876/41"
	raw, err := AttachURL("http://127.0.0.1:1/", id, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	a, err := ParseAttach(u.Query())
	if err != nil {
		t.Fatal(err)
	}
	if a.TileID != id {
		t.Fatalf("tile id %q, want %q", a.TileID, id)
	}
}

func TestAttachURLRejectsNonHTTPOrigin(t *testing.T) {
	if _, err := AttachURL("file:///tmp", "a/1", 80, 24); err == nil {
		t.Fatal("a file: origin must be refused")
	}
	if _, err := AttachURL("http:///nohost", "a/1", 80, 24); err == nil {
		t.Fatal("an origin with no host must be refused")
	}
}

// No tile id, nothing to attach to: the door refuses rather than opening a
// socket bound to nothing.
func TestParseAttachRequiresTileID(t *testing.T) {
	if _, err := ParseAttach(url.Values{QueryCols: {"80"}}); err == nil {
		t.Fatal("a bind with no tile_id must be an error")
	}
}

// Absent or garbage sizes mean "no opinion" (0), not a fabricated default:
// shellsvc.ClampSize is the one owner of the bounds.
func TestParseAttachSizesAreOptional(t *testing.T) {
	a, err := ParseAttach(url.Values{QueryTileID: {"a/1"}, QueryCols: {"nonsense"}})
	if err != nil {
		t.Fatal(err)
	}
	if a.Cols != 0 || a.Rows != 0 {
		t.Fatalf("unparseable sizes must be 0, got %+v", a)
	}
}

func TestControlRoundTrip(t *testing.T) {
	c, err := DecodeControl(EncodeResize(100, 30))
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != KindResize || c.Cols != 100 || c.Rows != 30 {
		t.Fatalf("resize round trip gave %+v", c)
	}
	c, err = DecodeControl(EncodeExit("session gone", true))
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != KindExit || c.Message != "session gone" || !c.SessionGone {
		t.Fatalf("exit round trip gave %+v", c)
	}
	// A clean end carries no verdict: the client cannot read "gone" out of
	// an ordinary detach.
	c, err = DecodeControl(EncodeExit("", false))
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != KindExit || c.Message != "" || c.SessionGone {
		t.Fatalf("clean exit round trip gave %+v", c)
	}
}

func TestDecodeControlRejectsUnknown(t *testing.T) {
	if _, err := DecodeControl([]byte(`{"kind":"kill"}`)); err == nil {
		t.Fatal("an unknown control kind must be an error, not a silent drop")
	}
	if _, err := DecodeControl([]byte("not json")); err == nil {
		t.Fatal("a non-JSON text frame must be an error")
	}
}
