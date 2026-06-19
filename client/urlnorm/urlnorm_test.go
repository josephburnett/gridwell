package urlnorm

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		// Bare hosts get https:// prepended.
		{"example.com", "https://example.com", false},
		{"  example.com ", "https://example.com", false},
		{"example.com/path", "https://example.com/path", false},
		{"example.com:8080", "https://example.com:8080", false},
		{"localhost", "https://localhost", false},
		{"localhost:8080/foo", "https://localhost:8080/foo", false},
		{"192.168.1.1:8080", "https://192.168.1.1:8080", false},

		// Userinfo must not make the password's colon read as a port
		// separator (regression): these are valid URLs the server accepts.
		{"https://user:pass@host.com", "https://user:pass@host.com", false},
		{"user@example.com", "https://user@example.com", false},
		{"http://user:pass@localhost:8080/x", "http://user:pass@localhost:8080/x", false},

		// Already has a valid scheme: kept as-is.
		{"https://example.com", "https://example.com", false},
		{"http://example.com", "http://example.com", false},
		{"HTTP://example.com", "HTTP://example.com", false},

		// Bad scheme: rejected.
		{"javascript:alert(1)", "", true},
		{"file:///etc/passwd", "", true},
		{"ftp://example.com", "", true},

		// Empty / whitespace only: rejected.
		{"", "", true},
		{"   ", "", true},

		// Single word without a dot: rejected (likely a typo).
		{"foo", "", true},
		{"example", "", true},
	}
	for _, c := range cases {
		got, err := Normalize(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("Normalize(%q) = %q, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Normalize(%q) error %v, want %q", c.in, err, c.want)
			continue
		}
		if got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
