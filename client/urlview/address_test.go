package urlview

import "testing"

// The page arm before the row's own address is the whole point: a served page
// is a url tile whose url_string is empty, so the obvious order opens nothing.
func TestAddress(t *testing.T) {
	const served = "https://host/content/tok/p1/"
	cases := []struct {
		name      string
		page      bool
		pageURL   string
		urlString string
		want      string
	}{
		{"a served page opens at the content API", true, served, "", served},
		{"a url tile opens at its own address", false, served, "https://a.test/", "https://a.test/"},
		{"an address-less url tile opens nothing", false, served, "", ""},
	}
	for _, c := range cases {
		if got := Address(c.page, c.pageURL, c.urlString); got != c.want {
			t.Errorf("%s: Address = %q, want %q", c.name, got, c.want)
		}
	}
}
