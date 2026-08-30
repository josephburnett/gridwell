// Package urlnorm normalizes user-typed URL strings so the WASM client can
// validate before submitting to the server's strict http/https check.
package urlnorm

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// urlInText matches an http(s) URL embedded in arbitrary text (terminal
// output). The body runs to the first whitespace or a character that can't sit
// inside a URL; trailing sentence punctuation is trimmed by FindURLs.
var urlInText = regexp.MustCompile(`https?://[^\s"'<>` + "`" + `]+`)

// URLSpan is one URL found in a line of text: Col0/Col1 are 1-based, inclusive
// column positions (xterm link-range convention) of its first and last byte.
// Byte offsets equal columns for the ASCII URLs shell output carries.
type URLSpan struct {
	Col0, Col1 int
	URL        string
}

// FindURLs locates every http(s) URL in a single line of text and returns each
// with its 1-based inclusive column span — the input the xterm link provider
// needs to make shell URLs clickable. Trailing punctuation commonly adjacent to
// a URL in prose (.,;:!?) and a single balanced-looking ")" are excluded.
func FindURLs(text string) []URLSpan {
	var out []URLSpan
	for _, loc := range urlInText.FindAllStringIndex(text, -1) {
		trimmed := strings.TrimRight(text[loc[0]:loc[1]], ".,;:!?")
		// Drop trailing ")" that close a paren the url never opened — the
		// wrapping ")" in "(see https://x)" or the extra ")" in
		// "(/wiki/Foo_(bar))" — while keeping balanced ones like "/Foo_(bar)".
		for strings.HasSuffix(trimmed, ")") && strings.Count(trimmed, ")") > strings.Count(trimmed, "(") {
			trimmed = strings.TrimRight(trimmed[:len(trimmed)-1], ".,;:!?")
		}
		if trimmed == "" {
			continue
		}
		out = append(out, URLSpan{Col0: loc[0] + 1, Col1: loc[0] + len(trimmed), URL: trimmed})
	}
	return out
}

// Normalize trims the input, prepends "https://" when no scheme is
// present, and validates the result is a plausible http(s) URL. Returns
// a user-facing error message on rejection.
func Normalize(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("please enter a URL")
	}
	if i := strings.Index(s, "://"); i >= 0 {
		scheme := strings.ToLower(s[:i])
		if scheme != "http" && scheme != "https" {
			return "", fmt.Errorf("only http and https URLs are allowed (got %q)", scheme)
		}
		rest := s[i+3:]
		if !looksLikeHost(hostPart(rest)) {
			return "", errors.New("that does not look like a URL")
		}
		return s, nil
	}
	host := hostPart(s)
	if !looksLikeHost(host) {
		return "", errors.New("please enter a valid URL (e.g. example.com)")
	}
	return "https://" + s, nil
}

// Candidate is one autocomplete entry: an address plus the page title the
// freeze captured (a url tile's alt_text; "" when never frozen).
type Candidate struct {
	URL, Title string
}

// Suggest ranks candidates against the user's partial input for the
// new-url modal's autocomplete. The query matches the ADDRESS
// (case-insensitively, ignoring a leading "http(s)://" and "www." on both
// sides — typing "git" matches "https://github.com") or the TITLE
// (case-insensitive substring — typing words from a page's title finds
// its url). A candidate whose comparable address starts with the input
// ranks before any other match (address substring or title hit); within a
// rank the input order is preserved (the caller passes most-relevant-first).
// Dedupe is by the comparable address, so
// scheme/www variants of one address collapse to a single suggestion.
// Empty input returns the first `limit` distinct candidates. Returns at
// most `limit` results (nil when limit <= 0).
func Suggest(input string, candidates []Candidate, limit int) []Candidate {
	if limit <= 0 {
		return nil
	}
	q := comparableURL(input)
	qTitle := strings.ToLower(strings.TrimSpace(input))
	seen := make(map[string]bool, len(candidates))
	var prefix, other []Candidate
	for _, c := range candidates {
		c.URL = strings.TrimSpace(c.URL)
		cmp := comparableURL(c.URL)
		if c.URL == "" || seen[cmp] {
			continue
		}
		seen[cmp] = true
		if q == "" {
			prefix = append(prefix, c)
			continue
		}
		switch idx := strings.Index(cmp, q); {
		case idx == 0:
			prefix = append(prefix, c)
		case idx > 0:
			other = append(other, c)
		case qTitle != "" && strings.Contains(strings.ToLower(c.Title), qTitle):
			other = append(other, c)
		}
	}
	out := append(prefix, other...)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// comparableURL lowercases s and strips a leading http(s):// scheme and a
// "www." host prefix, so autocomplete matches on the meaningful part of the
// address rather than boilerplate the user rarely types.
func comparableURL(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, p := range []string{"https://", "http://"} {
		if strings.HasPrefix(s, p) {
			s = s[len(p):]
			break
		}
	}
	return strings.TrimPrefix(s, "www.")
}

// hostPart returns the host portion of `host[:port][/path...]`, i.e.
// everything up to the first `/`, `?`, or `#`. Used by looksLikeHost
// so trailing path/query characters don't confuse the dot check.
func hostPart(s string) string {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '/', '?', '#':
			return s[:i]
		}
	}
	return s
}

// looksLikeHost is a deliberately lenient sanity check. We accept anything
// containing a dot (example.com, 192.168.1.1, foo.bar.baz) or the literal
// "localhost", optionally with a :port suffix. Internationalized domains
// and IPv6 literals are out of scope.
func looksLikeHost(s string) bool {
	if s == "" {
		return false
	}
	// Drop any userinfo ("user:pass@") before the port check, or the
	// password's colon gets mistaken for the port separator — which would
	// reject "user:pass@host.com" (a URL the server's http/https check
	// happily accepts).
	if at := strings.LastIndex(s, "@"); at >= 0 {
		s = s[at+1:]
	}
	if i := strings.LastIndex(s, ":"); i > 0 {
		s = s[:i]
	}
	if s == "localhost" {
		return true
	}
	return strings.Contains(s, ".")
}
