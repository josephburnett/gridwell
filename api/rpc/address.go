package rpc

import "strings"

// MatchAddress reports whether a declared pattern matches a whole address.
// The one rule: "*" matches any run of characters, none included and "/"
// among them, so one star spans a nested group's path; every other
// character, "?" too, is itself.
func MatchAddress(pattern, address string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == address
	}
	rest, ok := strings.CutPrefix(address, parts[0])
	if !ok {
		return false
	}
	last := parts[len(parts)-1]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(rest, p)
		if i < 0 {
			return false
		}
		rest = rest[i+len(p):]
	}
	return strings.HasSuffix(rest, last)
}
