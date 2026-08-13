package panestate

import (
	"encoding/json"
	"testing"
)

func TestAscentPushPopIsLIFO(t *testing.T) {
	s := New()
	s.PushAscent(Saved{Zoom: 1})
	s.PushAscent(Saved{Zoom: 2})
	if s.AscentDepth() != 2 {
		t.Fatalf("depth = %d, want 2", s.AscentDepth())
	}
	if got := s.PopAscent(); got == nil || got.Zoom != 2 {
		t.Fatalf("pop = %v, want the last pushed (Zoom 2)", got)
	}
	if got := s.PopAscent(); got == nil || got.Zoom != 1 {
		t.Fatalf("pop = %v, want Zoom 1", got)
	}
	if got := s.PopAscent(); got != nil {
		t.Fatalf("pop on empty = %v, want nil", got)
	}
}

// PeekAscent returns a mutable pointer so a descent can re-anchor the saved top
// in place — the embed-descent re-anchor path depends on this.
func TestPeekAscentMutatesInPlace(t *testing.T) {
	s := New()
	if s.PeekAscent() != nil {
		t.Fatal("peek on empty must be nil")
	}
	s.PushAscent(Saved{Zoom: 1})
	top := s.PeekAscent()
	if top == nil {
		t.Fatal("peek must return the top")
	}
	top.Anchor = "g/9"
	top.TextFocus = "t1"
	// The mutation must be visible on the next pop (same backing entry).
	got := s.PopAscent()
	if got.Anchor != "g/9" || got.TextFocus != "t1" {
		t.Fatalf("peek mutation didn't persist: %+v", got)
	}
}

// PopAscent returns a copy: a later push reuses the popped slot in the
// backing array, and the caller's held pointer must not see it. The ascent
// restore path holds the popped frame across an animated transition while
// new descents can happen — aliasing here would corrupt the restore.
func TestPoppedEntryIsStableAcrossReuse(t *testing.T) {
	s := New()
	s.PushAscent(Saved{Zoom: 1, Anchor: "a"})
	got := s.PopAscent()
	s.PushAscent(Saved{Zoom: 99, Anchor: "b"}) // reuses the slot
	if got.Zoom != 1 || got.Anchor != "a" {
		t.Fatalf("popped entry mutated by a later push: %+v", got)
	}
}

// Saved's JSON keys are a wire vocabulary (session-local today, but one of the
// three saved-viewport encodings — see the naming-drift issue). Pin them so a
// key change is a deliberate decision with a compat story, not a drive-by.
func TestSavedJSONKeysArePinned(t *testing.T) {
	b, err := json.Marshal(Saved{
		Cx: 1, Cy: 2, Zoom: 3, TextFocus: "t", TextMode: "rendered",
		TextScrollX: 4, TextScrollY: 5, Anchor: "a", Path: []string{"w"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"cx":1,"cy":2,"zoom":3,"text_focus":"t","text_mode":"rendered","text_scroll_x":4,"text_scroll_y":5,"anchor":"a","path":["w"]}`
	if string(b) != want {
		t.Fatalf("Saved JSON vocabulary changed:\n got %s\nwant %s", b, want)
	}
}

// The one-active-surface rule for grid framing (owner decision 2026-08-13,
// extending #249): among panes showing the same grid, only the FOCUSED one
// writes; sole viewers always write.
func TestFramingWriters(t *testing.T) {
	panes := []PaneGrid{
		{PaneID: "a", GridID: "g1"},
		{PaneID: "b", GridID: "g1"}, // shares g1 with a
		{PaneID: "c", GridID: "g2"}, // sole viewer
	}
	w := FramingWriters(panes, "a")
	if !w["a"] || w["b"] || !w["c"] {
		t.Errorf("focused=a: got %+v, want a and c writing, b passive", w)
	}
	w = FramingWriters(panes, "c")
	if w["a"] || w["b"] || !w["c"] {
		t.Errorf("focused=c: got %+v, want only c writing (g1 has no active surface)", w)
	}
	w = FramingWriters(nil, "x")
	if len(w) != 0 {
		t.Errorf("no panes: got %+v", w)
	}
}
