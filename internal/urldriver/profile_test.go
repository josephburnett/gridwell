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
