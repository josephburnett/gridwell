//go:build unix

package cli

import "testing"

// `gridwell status` says "already serving" with exit 0 exactly when the
// serve lock is held, and never disturbs a stale banner file.
func TestRunStatus(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GRIDWELL_HOME", dir)

	if code := RunStatus(nil); code != 1 {
		t.Errorf("status with no server = %d, want 1", code)
	}

	l, err := acquireServeLock(dir)
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
