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

func TestDurable(t *testing.T) {
	cases := []struct {
		name                    string
		page, possiblyEphemeral bool
		want                    bool
	}{
		{"a placed url tile", false, false, true},
		{"a page view is its plugin's", true, false, false},
		{"an ephemeral visit", false, true, false},
		{"not known yet counts as ephemeral", false, true, false},
	}
	for _, c := range cases {
		if got := Durable(c.page, c.possiblyEphemeral); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPersistFreeze(t *testing.T) {
	cases := []struct {
		name         string
		freeze, page bool
		jpeg         []byte
		url, title   string
		want         bool
	}{
		{"a frame alone persists", true, false, []byte{1}, "", "", true},
		{"an address alone persists", true, false, nil, "https://a.test/", "", true},
		{"a title alone persists", true, false, nil, "", "t", true},
		{"an empty capture never overwrites", true, false, nil, "", "", false},
		{"a page view persists nothing", true, true, []byte{1}, "https://a.test/", "t", false},
		{"an ephemeral ascent asked for no freeze", false, false, []byte{1}, "https://a.test/", "t", false},
	}
	for _, c := range cases {
		if got := PersistFreeze(c.freeze, c.page, c.jpeg, c.url, c.title); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
