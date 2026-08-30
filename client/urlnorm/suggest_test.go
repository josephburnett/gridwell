package urlnorm

import (
	"slices"
	"testing"
)

// urls extracts the URL column for comparisons.
func urls(cs []Candidate) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.URL)
	}
	return out
}

// fromURLs builds title-less candidates.
func fromURLs(us ...string) []Candidate {
	out := make([]Candidate, len(us))
	for i, u := range us {
		out[i] = Candidate{URL: u}
	}
	return out
}

func TestSuggest(t *testing.T) {
	cands := fromURLs(
		"https://github.com/josephburnett/gridwell",
		"https://news.ycombinator.com",
		"https://www.google.com/search",
		"http://github.com/explore",
		"https://github.com/josephburnett/gridwell", // dup of [0]
		"",                  // empty, skipped
		"  https://go.dev ", // trimmed
	)
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
			got := urls(Suggest(c.input, cands, c.limit))
			if !slices.Equal(got, c.want) {
				t.Errorf("Suggest(%q, …, %d) = %v, want %v", c.input, c.limit, got, c.want)
			}
		})
	}
}

// TestSuggestMatchesTitles: typing words from a page's title finds its url,
// and an address-prefix match still outranks a title hit.
func TestSuggestMatchesTitles(t *testing.T) {
	cands := []Candidate{
		{URL: "https://news.ycombinator.com", Title: "Hacker News"},
		{URL: "https://example.com/z", Title: "Weekly Planning Notes"},
		{URL: "https://hn.example.com", Title: ""}, // comparable starts with "hn"
	}
	got := Suggest("planning", cands, 5)
	if want := []string{"https://example.com/z"}; !slices.Equal(urls(got), want) {
		t.Errorf("title words: %v, want %v", urls(got), want)
	}
	if got[0].Title != "Weekly Planning Notes" {
		t.Errorf("the title rides the suggestion: %q", got[0].Title)
	}
	// Case-insensitive title match.
	if got := urls(Suggest("HACKER news", cands, 5)); !slices.Equal(got, []string{"https://news.ycombinator.com"}) {
		t.Errorf("case-insensitive title: %v", got)
	}
	// "news" prefixes news.ycombinator.com's comparable address and appears
	// in the other candidate's title — the address-prefix match ranks first
	// regardless of input order.
	both := Suggest("news", []Candidate{
		{URL: "https://example.com", Title: "Daily News"},
		{URL: "https://news.ycombinator.com", Title: ""},
	}, 5)
	want := []string{"https://news.ycombinator.com", "https://example.com"}
	if !slices.Equal(urls(both), want) {
		t.Errorf("prefix beats title: %v, want %v", urls(both), want)
	}
}

// TestSuggestPrefixBeatsSubstringAcrossOrder pins the rank rule independent of
// input order: a later prefix match must still outrank an earlier substring
// match.
func TestSuggestPrefixBeatsSubstringAcrossOrder(t *testing.T) {
	cands := fromURLs(
		"https://example.com/docs", // "doc" is a substring (after "example.com/")
		"https://docs.example.com", // comparable starts with "docs" → prefix
	)
	got := urls(Suggest("docs", cands, 5))
	want := []string{
		"https://docs.example.com",
		"https://example.com/docs",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Suggest = %v, want %v", got, want)
	}
}

// TestSuggestDedupesSchemeAndWwwVariants: the same address typed with
// different scheme/www boilerplate collapses to one suggestion (the first
// seen).
func TestSuggestDedupesSchemeAndWwwVariants(t *testing.T) {
	cands := fromURLs(
		"https://example.com",
		"http://example.com",      // same comparable form → dropped
		"https://www.example.com", // same comparable form → dropped
		"https://other.com",
	)
	got := urls(Suggest("example", cands, 5))
	want := []string{"https://example.com"}
	if !slices.Equal(got, want) {
		t.Errorf("Suggest = %v, want %v (scheme/www variants should collapse)", got, want)
	}
	// Empty query lists everything, still deduped to one example.com.
	all := urls(Suggest("", cands, 5))
	wantAll := []string{"https://example.com", "https://other.com"}
	if !slices.Equal(all, wantAll) {
		t.Errorf("Suggest(\"\") = %v, want %v", all, wantAll)
	}
}
