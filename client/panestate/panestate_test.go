package panestate

import (
	"encoding/json"
	"testing"
)

func TestNewHasNoCaret(t *testing.T) {
	s := New()
	if _, ok := s.Caret(); ok {
		t.Fatal("a new pane state must have no caret")
	}
	if s.AscentDepth() != 0 {
		t.Fatalf("new ascent depth = %d, want 0", s.AscentDepth())
	}
	if s.Selected != "" {
		t.Fatal("new state must be unselected")
	}
}

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

func TestCaretSetClear(t *testing.T) {
	s := New()
	s.SetCaret(42)
	off, ok := s.Caret()
	if !ok || off != 42 {
		t.Fatalf("Caret = (%d,%v), want (42,true)", off, ok)
	}
	// Offset 0 is a real caret position, distinct from "no caret".
	s.SetCaret(0)
	if off, ok := s.Caret(); !ok || off != 0 {
		t.Fatalf("caret 0 must read as present: (%d,%v)", off, ok)
	}
	s.ClearCaret()
	if _, ok := s.Caret(); ok {
		t.Fatal("ClearCaret must remove the caret")
	}
}

// The zero value reads as a caret at offset 0 — that hazard is WHY New exists
// (the doc says "construct with New"). Pin the difference so the sentinel
// can't be silently refactored away.
func TestZeroValueVsNew(t *testing.T) {
	var zero State
	if off, ok := zero.Caret(); !ok || off != 0 {
		t.Fatalf("documented zero-value hazard changed: Caret() = (%d,%v); update the State docstring", off, ok)
	}
	fresh := New()
	if _, ok := fresh.Caret(); ok {
		t.Fatal("New() must start with no caret")
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
