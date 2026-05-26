package urldriver

import (
	"context"
	"testing"
)

// fakeStore satisfies PreviewWriter for tests without depending on the
// real store package (avoids an import cycle).
type fakeStore struct {
	previews map[int64][]byte
	urls     map[int64]string
}

func (f *fakeStore) SetURLPreview(_ context.Context, tileID int64, jpeg []byte) error {
	if f.previews == nil {
		f.previews = map[int64][]byte{}
	}
	f.previews[tileID] = jpeg
	return nil
}

func (f *fakeStore) SetURLString(_ context.Context, tileID int64, newURL string) error {
	if f.urls == nil {
		f.urls = map[int64]string{}
	}
	f.urls[tileID] = newURL
	return nil
}

func TestDriverUnavailableWithoutBinary(t *testing.T) {
	d := New(&fakeStore{}, Config{
		Browser:        "chromium",
		BinaryOverride: "/no/such/chromium-binary",
	})
	if d.Available() {
		t.Fatal("Available() = true; want false when binary missing")
	}
	if _, err := d.OpenSession(1, "https://example.com", 800, 600); err != ErrUnavailable {
		t.Errorf("OpenSession on unavailable driver: got %v, want ErrUnavailable", err)
	}
}

func TestUnknownBrandUnavailable(t *testing.T) {
	d := New(&fakeStore{}, Config{Browser: "no-such-brand"})
	if d.Available() {
		t.Fatal("Available() = true; want false for unknown brand")
	}
}
