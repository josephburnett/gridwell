package auth

import (
	"strings"
	"testing"
)

// fastParams keeps argon2 cheap so tests are responsive. Production uses
// DefaultParams() which is ~64 MiB and noticeably slower.
func fastParams() *Params {
	return &Params{Time: 1, Memory: 8 * 1024, Threads: 1, KeyLen: 16, SaltLen: 8}
}

func TestHashAndVerifyRoundTrip(t *testing.T) {
	hash, err := HashPassword("hunter2", fastParams())
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := VerifyPassword("hunter2", hash); err != nil {
		t.Errorf("verify good: %v", err)
	}
	if err := VerifyPassword("wrong", hash); err == nil {
		t.Error("verify bad: expected error, got nil")
	}
}

func TestHashEmptyPasswordRejected(t *testing.T) {
	if _, err := HashPassword("", fastParams()); err == nil {
		t.Error("expected error for empty password")
	}
}

func TestHashUsesFreshSalt(t *testing.T) {
	a, err := HashPassword("same", fastParams())
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashPassword("same", fastParams())
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("identical passwords produced identical hashes — salt is not random")
	}
}

func TestVerifyRejectsMalformedHash(t *testing.T) {
	bads := []string{
		"",
		"plain text",
		"$argon2i$v=19$m=8192,t=1,p=1$xxxx$yyyy",                 // not argon2id
		"$argon2id$v=19$m=8192,t=1,p=1$not!base64$yyyy",          // bad salt
		"$argon2id$v=19$m=8192,t=1,p=1$xxxx$not!base64",          // bad key
		"$argon2id$v=99$m=8192,t=1,p=1$xxxx$yyyy",                // bad version
		"$argon2id$v=19$m=8192$xxxx$yyyy",                        // truncated params
		"$argon2id$v=19$m=8192,t=1,p=1$xxxx$yyyy$extra",          // extra field
	}
	for _, h := range bads {
		if err := VerifyPassword("anything", h); err == nil {
			t.Errorf("expected error for %q", h)
		}
	}
}

func TestHashFormatPrefix(t *testing.T) {
	h, err := HashPassword("a", fastParams())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$") {
		t.Errorf("missing argon2id prefix: %q", h)
	}
}

func TestDefaultParamsAreUsable(t *testing.T) {
	// Smoke: production parameters round-trip. Skipped under -short so the
	// fast tests stay fast.
	if testing.Short() {
		t.Skip("argon2 production params are slow; skipping under -short")
	}
	h, err := HashPassword("p", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPassword("p", h); err != nil {
		t.Errorf("verify: %v", err)
	}
}
