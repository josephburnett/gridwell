//go:build !js

package urldriver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultProfileDirIsGridwellOwned(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	for _, brand := range BrandNames() {
		got, err := DefaultProfileDir(brand)
		if err != nil {
			t.Errorf("%s: %v", brand, err)
			continue
		}
		want := filepath.Join(home, ".gridwell", "profiles", brand)
		if got != want {
			t.Errorf("%s profile dir = %q, want %q", brand, got, want)
		}
		// Sanity: must never resolve to a path the user's real browser
		// would touch (~/.config/<vendor> or
		// ~/Library/Application Support/<vendor>). The "~/.gridwell"
		// owned-prefix is the whole point of this design.
		if !strings.Contains(got, ".gridwell") {
			t.Errorf("%s profile dir %q must live under ~/.gridwell", brand, got)
		}
	}
}

func TestDefaultProfileDirRejectsUnknownBrand(t *testing.T) {
	if _, err := DefaultProfileDir("no-such-brand"); err == nil {
		t.Error("DefaultProfileDir should reject unknown brand")
	}
}

func TestResolveBinaryRejectsUnknownBrand(t *testing.T) {
	if _, err := ResolveBinary("no-such-brand", ""); err == nil {
		t.Error("ResolveBinary should reject unknown brand")
	}
}

func TestResolveBinaryHonorsOverride(t *testing.T) {
	// Use this test binary's own path as a stand-in for a real browser.
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	got, err := ResolveBinary("chromium", self)
	if err != nil {
		t.Fatalf("ResolveBinary: %v", err)
	}
	if got != self {
		t.Errorf("ResolveBinary = %q, want override %q", got, self)
	}
}

func TestResolveBinaryRejectsMissingOverride(t *testing.T) {
	if _, err := ResolveBinary("chromium", "/no/such/binary"); err == nil {
		t.Error("ResolveBinary should error when override path is missing")
	}
}

// makeProfileDirs creates a user-data-dir with the given sub-profile
// directories (plus a stray non-profile dir and a file, to prove they're
// ignored) and returns its path.
func makeProfileDirs(t *testing.T, profiles ...string) string {
	t.Helper()
	udd := t.TempDir()
	for _, p := range profiles {
		if err := os.MkdirAll(filepath.Join(udd, p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
	}
	// Decoys: a non-profile directory and a file that happens to look numbered.
	if err := os.MkdirAll(filepath.Join(udd, "GPUCache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(udd, "Local State"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	return udd
}

func TestResolveProfileDirectory(t *testing.T) {
	tests := []struct {
		name     string
		profiles []string
		want     string
	}{
		{
			name:     "only Default",
			profiles: []string{"Default"},
			want:     "Default",
		},
		{
			// The real-world case: a sign-in forked "Profile 1".
			name:     "Default plus one added profile",
			profiles: []string{"Default", "Profile 1"},
			want:     "Profile 1",
		},
		{
			name:     "highest-numbered profile wins",
			profiles: []string{"Default", "Profile 1", "Profile 2"},
			want:     "Profile 2",
		},
		{
			name:     "numeric not lexical ordering",
			profiles: []string{"Default", "Profile 2", "Profile 10"},
			want:     "Profile 10",
		},
		{
			name:     "Guest and System profiles are ignored",
			profiles: []string{"Default", "Profile 1", "Guest Profile", "System Profile"},
			want:     "Profile 1",
		},
		{
			name:     "added profile without a Default still resolves",
			profiles: []string{"Profile 1"},
			want:     "Profile 1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			udd := makeProfileDirs(t, tt.profiles...)
			if got := ResolveProfileDirectory(udd); got != tt.want {
				t.Errorf("ResolveProfileDirectory = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveProfileDirectoryFallsBackToDefault(t *testing.T) {
	// Unreadable / nonexistent user-data-dir.
	if got := ResolveProfileDirectory(filepath.Join(t.TempDir(), "does-not-exist")); got != "Default" {
		t.Errorf("missing user-data-dir: got %q, want Default", got)
	}
	// Exists but contains no profile directories at all.
	empty := t.TempDir()
	if err := os.MkdirAll(filepath.Join(empty, "GPUCache"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ResolveProfileDirectory(empty); got != "Default" {
		t.Errorf("no profiles: got %q, want Default", got)
	}
}

func TestBrandExtraFlagsBraveOnly(t *testing.T) {
	// brave gets disable-brave-update; the others have no extras today.
	flags := BrandExtraFlags("brave")
	wantSub := "disable-brave-update"
	found := false
	for _, f := range flags {
		if f == wantSub {
			found = true
		}
	}
	if !found {
		t.Errorf("brave flags %v missing %q", flags, wantSub)
	}
	for _, brand := range []string{"chromium", "chrome", "edge"} {
		if len(BrandExtraFlags(brand)) != 0 {
			t.Errorf("%s extra flags should be empty, got %v", brand, BrandExtraFlags(brand))
		}
	}
}
