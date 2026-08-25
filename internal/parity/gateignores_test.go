package parity

import (
	"os"
	"strings"
	"testing"
)

// The migrate script's parity gate must run the SAME blind-spot list as
// the in-repo whole-home gate — the list lives once, in
// ConvertGateIgnoreFields, and this pin catches the script drifting
// (2026-08-24: the script was missing menu_entries and the real
// migration failed with 2122 false differences).
func TestMigrateScriptUsesTheConvertGateIgnores(t *testing.T) {
	b, err := os.ReadFile("../../scripts/migrate-v2.sh")
	if err != nil {
		t.Fatal(err)
	}
	want := "--ignore-fields " + strings.Join(ConvertGateIgnoreFields, ",")
	if n := strings.Count(string(b), want); n < 2 {
		t.Fatalf("scripts/migrate-v2.sh must use %q for both the gate and the printed hint (found %d)", want, n)
	}
}
