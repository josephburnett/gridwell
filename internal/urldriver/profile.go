//go:build !js

package urldriver

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

// DefaultProfileDirectory is the Chrome sub-profile a fresh --user-data-dir
// starts with, and what Chrome selects when no --profile-directory is given.
const DefaultProfileDirectory = "Default"

// ResolveProfileDirectory picks which Chrome sub-profile inside userDataDir
// (e.g. "Default" or "Profile 1") to launch, and returns it for passing as
// --profile-directory.
//
// A Chrome --user-data-dir is a *container* of profiles, not a profile: it
// holds "Default" plus a "Profile N" directory for each additional profile.
// A headless launch with no --profile-directory always picks "Default", which
// may not be where the user did their interactive sign-in — Chrome can add a
// fresh profile when an account signs in. We close that gap with one generic
// heuristic: use the most recently created profile.
//
// Chrome creates "Default" first, then "Profile 1", "Profile 2", ... in
// increasing order, so the highest-numbered profile directory is the most
// recently created one. No site-, account-, or vendor-specific logic.
//
// Never fatal: if userDataDir can't be read or holds no profile directories,
// this returns "Default" — exactly what Chrome itself would launch.
func ResolveProfileDirectory(userDataDir string) string {
	entries, err := os.ReadDir(userDataDir)
	if err != nil {
		return DefaultProfileDirectory
	}
	best := DefaultProfileDirectory
	bestRank := -1
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if r := profileCreationRank(e.Name()); r > bestRank {
			bestRank = r
			best = e.Name()
		}
	}
	return best
}

// profileCreationRank orders Chrome profile directories by creation: "Default"
// is created first (rank 0), then "Profile 1", "Profile 2", ... (rank N), so a
// higher rank means more recently created. Directories that aren't Chrome
// profiles (e.g. "Guest Profile", "System Profile", caches) return -1.
func profileCreationRank(name string) int {
	if name == DefaultProfileDirectory {
		return 0
	}
	const prefix = "Profile "
	if strings.HasPrefix(name, prefix) {
		if n, err := strconv.Atoi(name[len(prefix):]); err == nil && n > 0 {
			return n
		}
	}
	return -1
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
