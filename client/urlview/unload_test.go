package urlview

import (
	"bytes"
	"testing"
)

func TestDecideUnloadURLStateTable(t *testing.T) {
	cases := []struct {
		name                    string
		page, durable, navDirty bool
		lastURL, cachedURL      string
		want                    Capture
		write                   bool
	}{
		{"navigated durable view writes what the bridge reported", false, true, true, "https://b.test/p", "https://a.test/",
			Capture{URL: "https://b.test/p", Title: "t"}, true},
		{"bridge reported no address, cached row stands in", false, true, true, "", "https://a.test/",
			Capture{URL: "https://a.test/", Title: "t"}, true},
		{"no address anywhere", false, true, true, "", "", Capture{}, false},
		// The unload has no frame, and a page's address and title are its
		// plugin's, so a page has nothing left to write.
		{"page view writes nothing at unload", true, true, true, "https://b.test/p", "https://a.test/", Capture{}, false},
		{"ephemeral visit never writes", false, false, true, "https://b.test/p", "https://a.test/", Capture{}, false},
		{"no navigation since place", false, true, false, "https://b.test/p", "https://a.test/", Capture{}, false},
		{"page view that did not navigate", true, true, false, "", "https://a.test/", Capture{}, false},
	}
	for _, c := range cases {
		got, write := DecideUnloadURLState(c.page, c.durable, c.navDirty, c.lastURL, "t", c.cachedURL)
		if write != c.write || (write && !sameCapture(got, c.want)) {
			t.Errorf("%s: = (%+v, %v), want (%+v, %v)", c.name, got, write, c.want, c.write)
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
	return bytes.Equal(a.JPEG, b.JPEG) && a.URL == b.URL && a.Title == b.Title && a.History == b.History
}

func TestWriteback(t *testing.T) {
	full := Capture{JPEG: []byte{1}, URL: "https://a.test/", Title: "t", History: "h"}
	cases := []struct {
		name         string
		freeze, page bool
		in           Capture
		want         Capture
		write        bool
	}{
		{"an internet tile writes all four", true, false, full, full, true},
		{"a frame alone writes", true, false, Capture{JPEG: []byte{1}}, Capture{JPEG: []byte{1}}, true},
		{"an address alone writes", true, false, Capture{URL: "https://a.test/"}, Capture{URL: "https://a.test/"}, true},
		{"a title alone writes", true, false, Capture{Title: "t"}, Capture{Title: "t"}, true},
		{"an empty capture never overwrites", true, false, Capture{}, Capture{}, false},
		{"a page writes its frame alone", true, true, full, Capture{JPEG: []byte{1}}, true},
		{"a page with no frame writes nothing", true, true, Capture{URL: "https://a.test/", Title: "t", History: "h"}, Capture{}, false},
		{"an ephemeral ascent asked for no freeze", false, false, full, Capture{}, false},
	}
	for _, c := range cases {
		got, write := Writeback(c.freeze, c.page, c.in)
		if write != c.write || (write && !sameCapture(got, c.want)) {
			t.Errorf("%s: = (%+v, %v), want (%+v, %v)", c.name, got, write, c.want, c.write)
		}
	}
}
