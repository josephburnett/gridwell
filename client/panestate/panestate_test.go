package panestate

import "testing"

func TestNewHasNoCaret(t *testing.T) {
	s := New()
	if _, ok := s.Caret(); ok {
		t.Fatal("a new pane state must have no caret")
	}
	if s.AscentDepth() != 0 {
		t.Fatalf("new ascent depth = %d, want 0", s.AscentDepth())
	}
	if s.Selected != "" || s.Dirty {
		t.Fatal("new state must be unselected and clean")
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
