package panelayout

import (
	"errors"
	"slices"
	"testing"
)

// Every leaf's rows, in tree order: the doors of its path, then the tile it
// is inside, each once.
func TestHeldIDs(t *testing.T) {
	blob := []byte(`{"v":1,"root":{"split":{"dir":"v","ratio":0.5,` +
		`"a":{"pane":{"id":"p1","anchor":"u/1","path":["u/4"],"text_focus":"u/7"}},` +
		`"b":{"split":{"dir":"h","ratio":0.5,` +
		`"a":{"pane":{"id":"p2","anchor":"u/1","path":["u/4","u/5"]}},` +
		`"b":{"pane":{"id":"p3","anchor":"u/1","text_focus":"u/9"}}}}}},"focus":"p1"}`)
	got, err := HeldIDs(blob)
	if err != nil {
		t.Fatalf("HeldIDs: %v", err)
	}
	if want := []string{"u/4", "u/7", "u/5", "u/9"}; !slices.Equal(got, want) {
		t.Errorf("HeldIDs = %v, want %v", got, want)
	}
}

// A place written in full holds its doors too, the content frame's included,
// so a deep place holds what its projection would.
func TestHeldIDsReadsThePlace(t *testing.T) {
	blob := []byte(`{"v":1,"root":{"pane":{"id":"p1",` +
		`"place":[{"g":"u/1"},{"d":"u/3"},{"d":"u/7","c":true}],"text_focus":"u/7"}},"focus":"p1"}`)
	got, err := HeldIDs(blob)
	if err != nil {
		t.Fatalf("HeldIDs: %v", err)
	}
	if want := []string{"u/3", "u/7"}; !slices.Equal(got, want) {
		t.Errorf("HeldIDs = %v, want %v", got, want)
	}
}

// On a version error both readers do nothing rather than act on a layout
// they half understand.
func TestHeldIDsRejectsANewerVersion(t *testing.T) {
	if _, err := HeldIDs([]byte(`{"v":999,"root":{}}`)); !errors.Is(err, ErrLayoutVersion) {
		t.Errorf("err = %v, want ErrLayoutVersion", err)
	}
	if _, err := HeldIDs([]byte(`not json`)); err == nil {
		t.Error("corrupt blob decoded without error")
	}
}
