package heyfake

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// E2EHostDir is the environment variable naming the directory TestE2EHost
// links the CLI into; unset, the test skips.
const E2EHostDir = "HEYFAKE_E2E_DIR"

// TestE2EHost serves one made-up HEY to a browser suite
// (apps/desktop/e2e-web/web-link-target.spec.ts): a thread set aside, so the
// Set Aside box lists a link to it in everything. It writes `ready` once the
// CLI answers and serves until stdin closes, so the suite's exit ends it.
func TestE2EHost(t *testing.T) {
	dir := os.Getenv(E2EHostDir)
	if dir == "" {
		t.Skip(E2EHostDir + " is unset: this is a browser suite's fixture")
	}
	c := NewIn(t, dir)
	c.SetBox("asidebox", Thread{TopicID: 301, Subject: "Kite day", Summary: "the kite flew sideways",
		From: "Wren", Email: "wren@example.com", Seen: true,
		Created: time.Date(2026, 1, 5, 14, 3, 0, 0, time.UTC)})
	if err := os.WriteFile(filepath.Join(dir, "ready"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}
