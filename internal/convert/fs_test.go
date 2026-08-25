package convert

import "testing"

// A root of "/" is a legal fs plugin config (a whole-machine browser),
// and the macOS root really does carry dotfiles ("/.nofollow"). The
// prefix-string shape this pins against built root+"/" — "//" — which no
// path starts with, so EVERY entry under a "/" root was refused.
func TestRelKeyUnderRootSlash(t *testing.T) {
	cases := []struct {
		path, root, want string
	}{
		{"/.nofollow", "/", ".nofollow"},
		{"/Users/joe/notes.md", "/", "Users/joe/notes.md"},
		{"/", "/", "."},
		{"/home/joe", "/home/joe", "."},
		{"/home/joe/sub/a.md", "/home/joe", "sub/a.md"},
	}
	for _, c := range cases {
		got, err := relKey(c.path, c.root)
		if err != nil {
			t.Errorf("relKey(%q, %q): %v", c.path, c.root, err)
			continue
		}
		if got != c.want {
			t.Errorf("relKey(%q, %q) = %q, want %q", c.path, c.root, got, c.want)
		}
	}
	for _, bad := range []string{"/etc/passwd", "/home/job/x", "/home"} {
		if got, err := relKey(bad, "/home/joe"); err == nil {
			t.Errorf("relKey(%q, /home/joe) = %q, want refusal", bad, got)
		}
	}
}
