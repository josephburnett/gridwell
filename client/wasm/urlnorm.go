package main

import (
	"errors"
	"fmt"
	"strings"
)

// normalizeURL trims the input, prepends "https://" when no scheme is
// present, and validates the result is a plausible http(s) URL. Returns
// a user-facing error message on rejection.
func normalizeURL(raw string) (string, error) {
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
	if i := strings.LastIndex(s, ":"); i > 0 {
		s = s[:i]
	}
	if s == "localhost" {
		return true
	}
	return strings.Contains(s, ".")
}
