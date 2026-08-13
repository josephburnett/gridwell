//go:build unix

package cli

import "testing"

// `gridwell status` is the desktop app's --no-server discovery verb: it
// must say "already serving" (exit 0) exactly when the serve lock is held,
// and "not serving" (exit 1) otherwise — and probing must never disturb a
// stale banner file's absence of a holder.
func TestRunStatus(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GRIDWELL_HOME", home)

	if code := RunStatus(nil); code != 1 {
		t.Errorf("status with no server = %d, want 1", code)
	}

	l, err := acquireServeLock(home)
	if err != nil {
		t.Fatal(err)
	}
	l.WriteBanner("gridwell: serving on 127.0.0.1:4242 (static=embedded plugins=1)")
	if code := RunStatus(nil); code != 0 {
		t.Errorf("status with a held lock = %d, want 0", code)
	}
	l.Release()

	if code := RunStatus(nil); code != 1 {
		t.Errorf("status after release = %d, want 1", code)
	}
}
