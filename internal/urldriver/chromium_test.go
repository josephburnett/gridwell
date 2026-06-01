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

func TestNewErrorsOnMissingBinary(t *testing.T) {
	_, err := New(&fakeStore{}, Config{
		Browser:        "chromium",
		BinaryOverride: "/no/such/chromium-binary",
	})
	if err == nil {
		t.Fatal("New returned nil; want error when binary missing")
	}
}

func TestNewErrorsOnUnknownBrand(t *testing.T) {
	_, err := New(&fakeStore{}, Config{Browser: "no-such-brand"})
	if err == nil {
		t.Fatal("New returned nil; want error for unknown brand")
	}
}

func TestBrandNamesEnumeratesAllBrands(t *testing.T) {
	got := BrandNames()
	want := map[string]bool{"chromium": true, "chrome": true, "brave": true, "edge": true}
	if len(got) != len(want) {
		t.Errorf("BrandNames length = %d, want %d", len(got), len(want))
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected brand %q", g)
		}
	}
}

func TestAvailableReportsNonNil(t *testing.T) {
	var d *Driver
	if d.Available() {
		t.Error("(*Driver)(nil).Available() should be false")
	}
	d = &Driver{}
	if !d.Available() {
		t.Error("non-nil Driver.Available() should be true")
	}
}
