package panelayout

import (
	"errors"
	"testing"
)

// TestTextFocusIDs: the one answer to "which content tiles does this blob
// reference" — every leaf's TextFocus, in tree order, empty ones skipped.
// Both sides of the ephemeral reap read this: the store's boot sweep spares
// these ids, the router's delete-time reap collects them.
func TestTextFocusIDs(t *testing.T) {
	blob := []byte(`{"v":1,"root":{"split":{"dir":"v","ratio":0.5,` +
		`"a":{"pane":{"id":"p1","anchor":"u/1","cx":0.5,"cy":0.5,"zoom":1,"text_focus":"u/7"}},` +
		`"b":{"split":{"dir":"h","ratio":0.5,` +
		`"a":{"pane":{"id":"p2","anchor":"u/1","cx":0.5,"cy":0.5,"zoom":1}},` +
		`"b":{"pane":{"id":"p3","anchor":"u/1","cx":0.5,"cy":0.5,"zoom":1,"text_focus":"u/9"}}}}}},"focus":"p1"}`)
	got, err := TextFocusIDs(blob)
	if err != nil {
		t.Fatalf("TextFocusIDs: %v", err)
	}
	if len(got) != 2 || got[0] != "u/7" || got[1] != "u/9" {
		t.Errorf("TextFocusIDs = %v, want [u/7 u/9]", got)
	}
}

// TestTextFocusIDsReadsTheProjectionNotThePlace: a leaf's Place stack can name
// a content frame too, but the projection field is what decides. Anything else
// would be a second derivation of the same fact, and a blob where they
// disagree would be reaped by one reader and protected by the other.
func TestTextFocusIDsReadsTheProjectionNotThePlace(t *testing.T) {
	blob := []byte(`{"v":1,"root":{"pane":{"id":"p1","cx":0,"cy":0,"zoom":1,` +
		`"place":[{"g":"u/1"},{"d":"u/7","c":true}]}},"focus":"p1"}`)
	got, err := TextFocusIDs(blob)
	if err != nil {
		t.Fatalf("TextFocusIDs: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("TextFocusIDs = %v, want none: text_focus is the one owner", got)
	}
}

// A blob from a newer Gridwell is a version error, never a guess: both readers
// must then do nothing rather than act on a half-understood layout.
func TestTextFocusIDsRejectsANewerVersion(t *testing.T) {
	if _, err := TextFocusIDs([]byte(`{"v":999,"root":{}}`)); !errors.Is(err, ErrLayoutVersion) {
		t.Errorf("err = %v, want ErrLayoutVersion", err)
	}
	if _, err := TextFocusIDs([]byte(`not json`)); err == nil {
		t.Error("corrupt blob decoded without error")
	}
}
