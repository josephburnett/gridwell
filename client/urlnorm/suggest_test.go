package urlnorm

import (
	"slices"
	"testing"
)

func TestSuggest(t *testing.T) {
	cands := []string{
		"https://github.com/josephburnett/gridwell",
		"https://news.ycombinator.com",
		"https://www.google.com/search",
		"http://github.com/explore",
		"https://github.com/josephburnett/gridwell", // dup of [0]
		"",                  // empty, skipped
		"  https://go.dev ", // trimmed
	}
	cases := []struct {
		name  string
		input string
		limit int
		want  []string
	}{
		{
			name:  "empty input returns distinct candidates in order, capped",
			input: "",
			limit: 3,
			want: []string{
				"https://github.com/josephburnett/gridwell",
				"https://news.ycombinator.com",
				"https://www.google.com/search",
			},
		},
		{
			name:  "scheme/www ignored: prefix match ranks before substring",
			input: "git",
			limit: 5,
			want: []string{
				// comparable forms start with "git" → prefix bucket, input order.
				"https://github.com/josephburnett/gridwell",
				"http://github.com/explore",
			},
		},
		{
			name:  "substring match when input is mid-address",
			input: "ycombinator",
			limit: 5,
			want:  []string{"https://news.ycombinator.com"},
		},
		{
			name:  "www. stripped on both sides",
			input: "google",
			limit: 5,
			want:  []string{"https://www.google.com/search"},
		},
		{
			name:  "case-insensitive",
			input: "GitHub",
			limit: 5,
			want: []string{
				"https://github.com/josephburnett/gridwell",
				"http://github.com/explore",
			},
		},
		{
			name:  "limit caps results",
			input: "git",
			limit: 1,
			want:  []string{"https://github.com/josephburnett/gridwell"},
		},
		{
			name:  "no match",
			input: "zzz",
			limit: 5,
			want:  nil,
		},
		{
			name:  "limit <= 0 returns nil",
			input: "git",
			limit: 0,
			want:  nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Suggest(c.input, cands, c.limit)
			if !slices.Equal(got, c.want) {
				t.Errorf("Suggest(%q, …, %d) = %v, want %v", c.input, c.limit, got, c.want)
			}
		})
	}
}

// TestSuggestPrefixBeatsSubstringAcrossOrder pins the rank rule independent of
// input order: a later prefix match must still outrank an earlier substring
// match.
func TestSuggestPrefixBeatsSubstringAcrossOrder(t *testing.T) {
	cands := []string{
		"https://example.com/docs", // "doc" is a substring (after "example.com/")
		"https://docs.example.com", // comparable starts with "docs" → prefix
	}
	got := Suggest("docs", cands, 5)
	want := []string{
		"https://docs.example.com",
		"https://example.com/docs",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Suggest = %v, want %v", got, want)
	}
}

// TestSuggestDedupesSchemeAndWwwVariants: the same address typed with different
// scheme/www boilerplate must collapse to one suggestion (the first seen), not
// crowd the list with look-alikes that differ only in characters the user
// rarely types.
func TestSuggestDedupesSchemeAndWwwVariants(t *testing.T) {
	cands := []string{
		"https://example.com",
		"http://example.com",      // same comparable form → dropped
		"https://www.example.com", // same comparable form → dropped
		"https://other.com",
	}
	got := Suggest("example", cands, 5)
	want := []string{"https://example.com"}
	if !slices.Equal(got, want) {
		t.Errorf("Suggest = %v, want %v (scheme/www variants should collapse)", got, want)
	}
	// Empty query lists everything, still deduped to one example.com.
	all := Suggest("", cands, 5)
	wantAll := []string{"https://example.com", "https://other.com"}
	if !slices.Equal(all, wantAll) {
		t.Errorf("Suggest(\"\") = %v, want %v", all, wantAll)
	}
}
