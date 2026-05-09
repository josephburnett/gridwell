package auth

import "testing"

func TestNewSessionTokenLengthAndHex(t *testing.T) {
	tok, err := NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	if want := SessionTokenBytes * 2; len(tok) != want {
		t.Errorf("token length = %d, want %d", len(tok), want)
	}
	for _, c := range tok {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("non-hex character %q", c)
		}
	}
}

func TestNewSessionTokenIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := range 256 {
		tok, err := NewSessionToken()
		if err != nil {
			t.Fatal(err)
		}
		if seen[tok] {
			t.Fatalf("collision at iteration %d: %s", i, tok)
		}
		seen[tok] = true
	}
}
