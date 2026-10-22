package urlview

import (
	"bytes"
	"testing"
)

func TestDecideUnloadURLStateTable(t *testing.T) {
	cases := []struct {
		name           string
		owns, navDirty bool
		want           Capture
		write          bool
	}{
		// A scratch-grid row and a placed one alike: an ephemeral visit is
		// a url view like any other.
		{"a navigated view writes its title", true, true, Capture{Title: "t"}, true},
		// The unload has no frame, and an unowned row's title is its
		// plugin's, so it has nothing left to write.
		{"unowned row writes nothing at unload", false, true, Capture{}, false},
		{"no navigation since place", true, false, Capture{}, false},
	}
	for _, c := range cases {
		got, write := DecideUnloadURLState(c.owns, c.navDirty, "t")
		if write != c.write || (write && !sameCapture(got, c.want)) {
			t.Errorf("%s: = (%+v, %v), want (%+v, %v)", c.name, got, write, c.want, c.write)
		}
	}
	if _, write := DecideUnloadURLState(true, true, ""); write {
		t.Error("an untitled page wrote an empty capture")
	}
}

// The address is content and the verdict is the one place that says when a
// landed address becomes a write, wherever the row sits, a plugin's url
// included: never for a served page, never for what the store refuses, and
// never for the address already stored.
func TestWriteAddress(t *testing.T) {
	const a, b = "https://a.test/", "https://b.test/p"
	cases := []struct {
		name           string
		page           bool
		landed, stored string
		want           bool
	}{
		{"a url writes where it landed", false, b, a, true},
		{"landing where the row points writes nothing", false, a, a, false},
		{"a served page writes nothing", true, b, a, false},
		{"a non-http landing writes nothing", false, "about:blank", a, false},
		{"an error page writes nothing", false, "chrome-error://chromewebdata/", a, false},
		{"no landing writes nothing", false, "", a, false},
		{"an unconfigured row takes its first address", false, b, "", true},
	}
	for _, c := range cases {
		if got := WriteAddress(c.page, c.landed, c.stored); got != c.want {
			t.Errorf("%s: = %v, want %v", c.name, got, c.want)
		}
	}
}

// A served page's title and trail are its plugin's, and so are any row's in
// an unwritable grid; a grid not yet read is attempted.
func TestOwns(t *testing.T) {
	cases := []struct {
		name                  string
		page, writable, known bool
		want                  bool
	}{
		{"an internet tile in a writable grid", false, true, true, true},
		{"a served page", true, true, true, false},
		{"a plugin's url entry", false, false, true, false},
		{"a link target whose grid is unread", false, false, false, true},
	}
	for _, c := range cases {
		if got := Owns(c.page, c.writable, c.known); got != c.want {
			t.Errorf("%s: = %v, want %v", c.name, got, c.want)
		}
	}
}

func sameCapture(a, b Capture) bool {
	return bytes.Equal(a.JPEG, b.JPEG) && a.Title == b.Title && a.History == b.History
}

func TestWriteback(t *testing.T) {
	full := Capture{JPEG: []byte{1}, Title: "t", History: "h"}
	cases := []struct {
		name         string
		freeze, owns bool
		in           Capture
		want         Capture
		write        bool
	}{
		{"an owned row writes all three", true, true, full, full, true},
		{"a frame alone writes", true, true, Capture{JPEG: []byte{1}}, Capture{JPEG: []byte{1}}, true},
		{"a title alone writes", true, true, Capture{Title: "t"}, Capture{Title: "t"}, true},
		{"a trail alone writes", true, true, Capture{History: "h"}, Capture{History: "h"}, true},
		{"an empty capture never overwrites", true, true, Capture{}, Capture{}, false},
		{"an unowned row writes its frame alone", true, false, full, Capture{JPEG: []byte{1}}, true},
		{"an unowned row with no frame writes nothing", true, false, Capture{Title: "t", History: "h"}, Capture{}, false},
		{"a close asked for no freeze", false, true, full, Capture{}, false},
	}
	for _, c := range cases {
		got, write := Writeback(c.freeze, c.owns, c.in)
		if write != c.write || (write && !sameCapture(got, c.want)) {
			t.Errorf("%s: = (%+v, %v), want (%+v, %v)", c.name, got, write, c.want, c.write)
		}
	}
}
