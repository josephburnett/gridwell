package urlview

import (
	"bytes"
	"testing"
)

func TestDecideUnloadURLStateTable(t *testing.T) {
	cases := []struct {
		name                    string
		owns, durable, navDirty bool
		want                    Capture
		write                   bool
	}{
		{"navigated durable view writes its title", true, true, true, Capture{Title: "t"}, true},
		// The unload has no frame, and an unowned row's title is its
		// plugin's, so it has nothing left to write.
		{"unowned row writes nothing at unload", false, true, true, Capture{}, false},
		{"ephemeral visit never writes", true, false, true, Capture{}, false},
		{"no navigation since place", true, true, false, Capture{}, false},
	}
	for _, c := range cases {
		got, write := DecideUnloadURLState(c.owns, c.durable, c.navDirty, "t")
		if write != c.write || (write && !sameCapture(got, c.want)) {
			t.Errorf("%s: = (%+v, %v), want (%+v, %v)", c.name, got, write, c.want, c.write)
		}
	}
	if _, write := DecideUnloadURLState(true, true, true, ""); write {
		t.Error("an untitled page wrote an empty capture")
	}
}

// The address is content and the verdict is the one place that says when a
// landed address becomes a write: never for an ephemeral visit, never for a
// row the node does not own, never for what the store refuses, and never for
// the address already stored.
func TestWriteAddress(t *testing.T) {
	const a, b = "https://a.test/", "https://b.test/p"
	cases := []struct {
		name           string
		durable, owns  bool
		landed, stored string
		want           bool
	}{
		{"a durable owned row writes where it landed", true, true, b, a, true},
		{"landing where the row points writes nothing", true, true, a, a, false},
		{"an ephemeral visit writes nothing", false, true, b, a, false},
		{"an unowned row writes nothing", true, false, b, a, false},
		{"a non-http landing writes nothing", true, true, "about:blank", a, false},
		{"an error page writes nothing", true, true, "chrome-error://chromewebdata/", a, false},
		{"no landing writes nothing", true, true, "", a, false},
		{"an unconfigured row takes its first address", true, true, b, "", true},
	}
	for _, c := range cases {
		if got := WriteAddress(c.durable, c.owns, c.landed, c.stored); got != c.want {
			t.Errorf("%s: = %v, want %v", c.name, got, c.want)
		}
	}
}

// A served page's address is its plugin's, and so is any row in an
// unwritable grid; a grid not yet read is attempted.
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

// A page is durable exactly when an internet tile is: the page flag plays no
// part, so Freeze Page, the standing freeze and the zoom follow for both.
func TestDurable(t *testing.T) {
	if !Durable(false) {
		t.Error("a placed tile is not durable")
	}
	if Durable(true) {
		t.Error("a possibly ephemeral visit is durable")
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
		{"an ephemeral ascent asked for no freeze", false, true, full, Capture{}, false},
	}
	for _, c := range cases {
		got, write := Writeback(c.freeze, c.owns, c.in)
		if write != c.write || (write && !sameCapture(got, c.want)) {
			t.Errorf("%s: = (%+v, %v), want (%+v, %v)", c.name, got, write, c.want, c.write)
		}
	}
}
