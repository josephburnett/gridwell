package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClearBrowserData(t *testing.T) {
	t.Run("removes the gridwell partition", func(t *testing.T) {
		ud := t.TempDir()
		part := filepath.Join(ud, "Partitions", "gridwell")
		if err := os.MkdirAll(filepath.Join(part, "Local Storage"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(part, "Cookies"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		if code := clearBrowserData(&out, ud); code != 0 {
			t.Fatalf("exit %d: %s", code, out.String())
		}
		if _, err := os.Stat(part); !os.IsNotExist(err) {
			t.Fatalf("partition still present: %v", err)
		}
	})

	t.Run("no partition is a clean no-op", func(t *testing.T) {
		var out strings.Builder
		if code := clearBrowserData(&out, t.TempDir()); code != 0 {
			t.Fatalf("exit %d: %s", code, out.String())
		}
		if !strings.Contains(out.String(), "nothing to clear") {
			t.Fatalf("output %q", out.String())
		}
	})

	t.Run("refuses while the app holds the profile", func(t *testing.T) {
		ud := t.TempDir()
		part := filepath.Join(ud, "Partitions", "gridwell")
		if err := os.MkdirAll(part, 0o755); err != nil {
			t.Fatal(err)
		}
		// Chromium's lock is a symlink on Linux. A dangling one still counts
		// as running for a safe CLI, and Lstat sees it.
		if err := os.Symlink("host-123", filepath.Join(ud, "SingletonLock")); err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		if code := clearBrowserData(&out, ud); code != 1 {
			t.Fatalf("exit %d, want refusal: %s", code, out.String())
		}
		if _, err := os.Stat(part); err != nil {
			t.Fatalf("refusal must not touch the partition: %v", err)
		}
	})
}
