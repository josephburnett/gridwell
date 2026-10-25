package rpc

import "testing"

func TestMatchAddress(t *testing.T) {
	const mr = "https://gitlab.com/*/-/merge_requests/*"
	for _, tc := range []struct {
		pattern, address string
		want             bool
	}{
		{mr, "https://gitlab.com/group/project/-/merge_requests/12", true},
		{mr, "https://gitlab.com/group/sub/project/-/merge_requests/12", true}, // * crosses "/"
		{mr, "https://gitlab.com/group/project/-/merge_requests/12/diffs?view=inline", true},
		{mr, "https://gitlab.com/group/project/-/issues/12", false},
		{mr, "http://gitlab.com/group/project/-/merge_requests/12", false}, // the scheme is literal
		{mr, "https://gitlab.com.evil.test/x/-/merge_requests/1", false},
		{mr, "https://evil.test/?https://gitlab.com/x/-/merge_requests/1", false}, // anchored at the start
		{"https://example.test/*", "https://example.test/", true},                 // * matches nothing too
		{"https://example.test/*", "https://example.test", false},
		{"https://example.test/a", "https://example.test/a", true},
		{"https://example.test/a", "https://example.test/ab", false},   // anchored at the end
		{"https://example.test/a?b", "https://example.test/a?b", true}, // ? is literal
		{"https://example.test/a?b", "https://example.test/axb", false},
		{"*", "anything at all", true},
		{"*a*b*", "xxaxxbxx", true},
		{"*a*b*", "xxbxxaxx", false},
		{"", "", true},
		{"", "x", false},
	} {
		if got := MatchAddress(tc.pattern, tc.address); got != tc.want {
			t.Errorf("MatchAddress(%q, %q) = %v, want %v", tc.pattern, tc.address, got, tc.want)
		}
	}
}
