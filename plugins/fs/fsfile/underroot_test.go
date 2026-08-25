package fsfile

import "testing"

// UnderRoot must have no root=="/" edge: the hand-built root+"/" prefix
// check it replaces produced "//" there and confined nothing.
func TestUnderRoot(t *testing.T) {
	cases := []struct {
		root, path string
		ok         bool
	}{
		{"/", "/.nofollow", true},
		{"/", "/", true},
		{"/home/joe", "/home/joe", true},
		{"/home/joe", "/home/joe/sub/a.md", true},
		{"/home/joe", "/home/job/a.md", false},
		{"/home/joe", "/home", false},
		{"/home/joe", "/etc/passwd", false},
	}
	for _, c := range cases {
		if got := UnderRoot(c.root, c.path); got != c.ok {
			t.Errorf("UnderRoot(%q, %q) = %v, want %v", c.root, c.path, got, c.ok)
		}
	}
}
