package node

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A retired kind name never returns (owner decisions 2026-08-16 and
// 2026-08-27: localdb/local → home, ssh → remote). Init used to accept ANY
// kind string: "local" is not native, so no DB was created, a `kind:
// local` entry was appended, and the next serve refused the config —
// while BuildConfig's own guidance told the user to run exactly that.
func TestInitRefusesRetiredKindsBeforeWriting(t *testing.T) {
	for retired := range RenamedKinds {
		home := t.TempDir()
		if _, err := Init(home, retired, "x", nil); err == nil {
			t.Fatalf("Init(kind=%q) succeeded; a retired name must be refused", retired)
		} else if !strings.Contains(err.Error(), RenamedKinds[retired]) {
			t.Errorf("Init(kind=%q) error %q does not name the current kind %q", retired, err, RenamedKinds[retired])
		}
		entries, _ := os.ReadDir(home)
		if len(entries) != 0 {
			t.Fatalf("Init(kind=%q) wrote %v before refusing", retired, entries)
		}
	}
}

// The "no config" guidance must name a kind Init accepts as native — the
// user is going to paste it.
func TestNoConfigGuidanceNamesANativeKind(t *testing.T) {
	home := t.TempDir()
	kindRe := regexp.MustCompile(`--kind (\S+)`)
	check := func(err error) {
		t.Helper()
		if err == nil {
			t.Fatal("expected an error")
		}
		m := kindRe.FindStringSubmatch(err.Error())
		if m == nil {
			t.Fatalf("guidance %q names no --kind", err)
		}
		if !IsNative(m[1]) {
			t.Fatalf("guidance %q names kind %q, which init would not create a DB for", err, m[1])
		}
	}
	cfgPath := filepath.Join(home, "server.yaml")
	_, err := BuildConfig(home, cfgPath)
	check(err)
	if err := os.WriteFile(cfgPath, []byte("plugins: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = BuildConfig(home, cfgPath)
	check(err)
}
