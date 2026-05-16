package urldriver

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// fakeStore satisfies PreviewWriter for tests without depending on the
// real store package (avoids an import cycle).
type fakeStore struct {
	previews map[int64][]byte
	urls     map[int64]string
}

func (f *fakeStore) SetURLPreview(_ context.Context, _, tileID int64, jpeg []byte) error {
	if f.previews == nil {
		f.previews = map[int64][]byte{}
	}
	f.previews[tileID] = jpeg
	return nil
}

func (f *fakeStore) SetURLString(_ context.Context, _, tileID int64, newURL string) error {
	if f.urls == nil {
		f.urls = map[int64]string{}
	}
	f.urls[tileID] = newURL
	return nil
}

func TestDriverUnavailableWithoutBinary(t *testing.T) {
	// Force "binary not found": pass a path that doesn't exist.
	d := New(&fakeStore{}, Config{
		BinaryPath:  filepath.Join(t.TempDir(), "no-such-chromium"),
		ProfileRoot: t.TempDir(),
	})
	if d.Available() {
		t.Fatal("Available() = true; want false when binary missing")
	}
	if err := d.Wake(context.Background(), 1, 1, "https://example.com"); err != ErrUnavailable {
		t.Errorf("Wake on unavailable driver: got %v, want ErrUnavailable", err)
	}
	if d.IsLive(1, 1) {
		t.Error("IsLive=true on unavailable driver")
	}
}

func TestDriverUnavailableWithoutProfileRoot(t *testing.T) {
	d := New(&fakeStore{}, Config{
		// BinaryPath empty → auto-detect; for the test we don't care
		// because ProfileRoot is empty which also makes us unavailable.
		ProfileRoot: "",
	})
	if d.Available() {
		t.Fatal("Available() = true; want false when ProfileRoot empty")
	}
}

func TestDriverCreatesProfileRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "subdir", "chromium")
	// Force binary detection failure (so Available is false, but only
	// because of the binary, not the root) — we just want to verify
	// that the constructor MkdirAlls the root when it's available.
	binary := filepath.Join(t.TempDir(), "fake-bin")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := New(&fakeStore{}, Config{
		BinaryPath:  binary,
		ProfileRoot: root,
	})
	if !d.Available() {
		t.Fatal("Available() = false; want true with binary present and root writable")
	}
	if _, err := os.Stat(root); err != nil {
		t.Errorf("profile root not created: %v", err)
	}
}
