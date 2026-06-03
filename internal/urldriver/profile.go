//go:build !js

package urldriver

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// DefaultProfileDir returns the gridwell-owned user-data-dir for the
// given browser brand: ~/.gridwell/profiles/<brand>. Same path on every
// supported OS — no XDG / Library/Application Support divergence. This
// is the location both `gridwell serve` and `gridwell open-browser` use
// by default, so an interactive login in open-browser leaves cookies the
// headless serve session reads back.
//
// Returns an error if the brand is unknown or $HOME can't be resolved.
func DefaultProfileDir(brandName string) (string, error) {
	if _, ok := brands[brandName]; !ok {
		return "", fmt.Errorf("unknown browser %q (known: %v)", brandName, brandNames())
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve $HOME: %w", err)
	}
	return filepath.Join(home, ".gridwell", "profiles", brandName), nil
}

// ResolveBinary returns the path to the browser binary for the given
// brand. If override is non-empty, it is used directly (with a stat
// check); otherwise the brand's candidate list is walked via exec.LookPath.
// Returns an error if nothing is found — callers should never silently
// proceed without a binary.
func ResolveBinary(brandName, override string) (string, error) {
	b, ok := brands[brandName]
	if !ok {
		return "", fmt.Errorf("unknown browser %q (known: %v)", brandName, brandNames())
	}
	if override != "" {
		if _, err := os.Stat(override); err != nil {
			return "", fmt.Errorf("browser binary not found at %q: %w", override, err)
		}
		return override, nil
	}
	for _, name := range b.binaryNames {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s binary not found on PATH (tried %v)", brandName, b.binaryNames)
}

// BrandExtraFlags returns the CLI flags Gridwell adds for the given
// brand (e.g. Brave's "disable-brave-update"). Used by both the URL
// driver's headless launch and the open-browser headful launch so the
// two share the same flag baseline.
func BrandExtraFlags(brandName string) []string {
	b, ok := brands[brandName]
	if !ok {
		return nil
	}
	// Copy so callers can't mutate the registry.
	out := make([]string, len(b.extraFlags))
	copy(out, b.extraFlags)
	return out
}
