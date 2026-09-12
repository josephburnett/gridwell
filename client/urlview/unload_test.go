package urlview

import "testing"

func TestDecideUnloadURLStateTable(t *testing.T) {
	cases := []struct {
		name                    string
		page, durable, navDirty bool
		lastURL, cachedURL      string
		wantURL                 string
		want                    bool
	}{
		{"navigated durable view writes what the bridge reported", false, true, true, "https://b.test/p", "https://a.test/", "https://b.test/p", true},
		{"bridge reported no address, cached row stands in", false, true, true, "", "https://a.test/", "https://a.test/", true},
		{"no address anywhere", false, true, true, "", "", "", false},
		{"page view never writes", true, true, true, "https://b.test/p", "https://a.test/", "", false},
		{"ephemeral visit never writes", false, false, true, "https://b.test/p", "https://a.test/", "", false},
		{"no navigation since place", false, true, false, "https://b.test/p", "https://a.test/", "", false},
		{"page view that did not navigate", true, true, false, "", "https://a.test/", "", false},
	}
	for _, c := range cases {
		url, write := DecideUnloadURLState(c.page, c.durable, c.navDirty, c.lastURL, c.cachedURL)
		if url != c.wantURL || write != c.want {
			t.Errorf("%s: = (%q, %v), want (%q, %v)", c.name, url, write, c.wantURL, c.want)
		}
	}
}
