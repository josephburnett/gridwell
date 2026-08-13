//go:build unix

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The one-serve-per-home guard: an flock, so a second acquire fails while
// the first is held — even within one process (flock conflicts across open
// file descriptions) — carries the holder's banner for the "already
// serving" reprint, and dies with the holder (Release here; the kernel on
// a crash).
func TestServeLock(t *testing.T) {
	home := t.TempDir()

	l1, err := acquireServeLock(home)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	l1.WriteBanner("gridwell: serving on 127.0.0.1:1234 (static=embedded plugins=1)")

	_, err = acquireServeLock(home)
	var held *errServeLockHeld
	if !errors.As(err, &held) {
		t.Fatalf("second acquire = %v, want errServeLockHeld", err)
	}
	if held.banner != "gridwell: serving on 127.0.0.1:1234 (static=embedded plugins=1)" {
		t.Errorf("held banner = %q, want the holder's banner", held.banner)
	}

	l1.Release()
	if _, err := os.Stat(filepath.Join(home, "serve.lock")); !os.IsNotExist(err) {
		t.Errorf("serve.lock survives Release (stat err=%v); a leftover must mean a crash", err)
	}
	l3, err := acquireServeLock(home)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	defer l3.Release()
}

// A leftover file from a CRASHED holder (flock gone, content stale) must
// not block the next serve — the flock is the gate, the file is just the
// banner.
func TestServeLockStaleFile(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "serve.lock"),
		[]byte("gridwell: serving on 127.0.0.1:9 (static=embedded plugins=1)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := acquireServeLock(home)
	if err != nil {
		t.Fatalf("acquire over stale file: %v", err)
	}
	defer l.Release()
	// The stale banner was truncated away — a conflicting serve must never
	// reprint a dead holder's address.
	b, err := os.ReadFile(filepath.Join(home, "serve.lock"))
	if err != nil || len(b) != 0 {
		t.Errorf("stale banner not truncated: %q err=%v", b, err)
	}
}
