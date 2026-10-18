//go:build unix

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A second acquire fails while the first is held, even within one process,
// because flock conflicts across open file descriptions. The lock carries
// the holder's banner and dies with the holder.
func TestServeLock(t *testing.T) {
	dir := t.TempDir()

	l1, err := acquireServeLock(dir)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	l1.WriteBanner("gridwell: serving on 127.0.0.1:1234 (static=embedded plugins=1)")

	_, err = acquireServeLock(dir)
	var held *errServeLockHeld
	if !errors.As(err, &held) {
		t.Fatalf("second acquire = %v, want errServeLockHeld", err)
	}
	if held.banner != "gridwell: serving on 127.0.0.1:1234 (static=embedded plugins=1)" {
		t.Errorf("held banner = %q, want the holder's banner", held.banner)
	}

	l1.Release()
	if _, err := os.Stat(filepath.Join(dir, "serve.lock")); !os.IsNotExist(err) {
		t.Errorf("serve.lock survives Release (stat err=%v); a leftover must mean a crash", err)
	}
	l3, err := acquireServeLock(dir)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	defer l3.Release()
}

// A crashed holder's leftover file must not block the next serve: the flock
// is the gate and the file is just the banner.
func TestServeLockStaleFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "serve.lock"),
		[]byte("gridwell: serving on 127.0.0.1:9 (static=embedded plugins=1)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := acquireServeLock(dir)
	if err != nil {
		t.Fatalf("acquire over stale file: %v", err)
	}
	defer l.Release()
	// A conflicting serve must never reprint a dead holder's address.
	b, err := os.ReadFile(filepath.Join(dir, "serve.lock"))
	if err != nil || len(b) != 0 {
		t.Errorf("stale banner not truncated: %q err=%v", b, err)
	}
}

// The status probe is read-only: a held lock reports the banner, a crashed
// holder's leftover reports not-serving, and the file survives either way.
func TestProbeServeLockReadOnly(t *testing.T) {
	dir := t.TempDir()

	if _, running, err := probeServeLock(dir); err != nil || running {
		t.Fatalf("probe of empty node directory = (%v, %v), want not running", running, err)
	}

	l, err := acquireServeLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	l.WriteBanner("gridwell: serving on 127.0.0.1:7 (static=embedded plugins=1)")
	banner, running, err := probeServeLock(dir)
	if err != nil || !running || banner != "gridwell: serving on 127.0.0.1:7 (static=embedded plugins=1)" {
		t.Fatalf("probe of held lock = (%q, %v, %v)", banner, running, err)
	}
	// Probing did not disturb the holder.
	if _, err := acquireServeLock(dir); err == nil {
		t.Fatal("holder lost the lock to a probe")
	}
	l.Release()

	// File present, flock gone: not running, and the file survives.
	if err := os.WriteFile(filepath.Join(dir, "serve.lock"), []byte("stale\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, running, err := probeServeLock(dir); err != nil || running {
		t.Fatalf("probe of stale file = (%v, %v), want not running", running, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "serve.lock")); err != nil {
		t.Errorf("probe removed the crash breadcrumb: %v", err)
	}
}
