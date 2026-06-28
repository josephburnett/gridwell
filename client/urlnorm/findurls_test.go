package urlnorm

import (
	"reflect"
	"testing"
)

func TestFindURLs(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []URLSpan
	}{
		{"none", "no links here", nil},
		{
			"bare url at start",
			"https://example.com/x rest",
			[]URLSpan{{Col0: 1, Col1: 21, URL: "https://example.com/x"}},
		},
		{
			"url mid-line with offset columns",
			"see http://a.test/p now",
			// "see " is 4 cols, url starts at col 5, ends at col 19 (15 chars).
			[]URLSpan{{Col0: 5, Col1: 19, URL: "http://a.test/p"}},
		},
		{
			"trailing sentence punctuation trimmed",
			"go to https://example.com/page.",
			[]URLSpan{{Col0: 7, Col1: 30, URL: "https://example.com/page"}},
		},
		{
			"wrapping paren trimmed but balanced parens kept",
			"(https://en.wikipedia.org/wiki/Foo_(bar))",
			[]URLSpan{{Col0: 2, Col1: 40, URL: "https://en.wikipedia.org/wiki/Foo_(bar)"}},
		},
		{
			"two urls on one line",
			"a https://x.test b https://y.test",
			[]URLSpan{
				{Col0: 3, Col1: 16, URL: "https://x.test"},
				{Col0: 20, Col1: 33, URL: "https://y.test"},
			},
		},
		{"non-http scheme ignored", "ftp://nope.test and file:///x", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FindURLs(c.text)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("FindURLs(%q) = %+v, want %+v", c.text, got, c.want)
			}
		})
	}
}
