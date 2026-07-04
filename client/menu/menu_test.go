package menu

import "testing"

func TestNewIsClosed(t *testing.T) {
	s := New()
	if s.IsOpen() {
		t.Fatal("new menu must be closed")
	}
	if s.OpenOn("p1") {
		t.Fatal("new menu must not be open on any pane")
	}
	if got := s.PaneID(); got != "" {
		t.Fatalf("closed menu PaneID = %q, want empty", got)
	}
	if got := s.Hover(); got != -1 {
		t.Fatalf("new menu Hover = %d, want -1", got)
	}
}

func TestOpenTargetsOnePane(t *testing.T) {
	s := New()
	s.Open("p1")
	if !s.IsOpen() {
		t.Fatal("Open must leave the menu open")
	}
	if !s.OpenOn("p1") {
		t.Fatal("menu must be open on the opened pane")
	}
	if s.OpenOn("p2") {
		t.Fatal("menu must NOT be open on a different pane")
	}
	if got := s.PaneID(); got != "p1" {
		t.Fatalf("PaneID = %q, want p1", got)
	}
}

func TestOpenResetsHover(t *testing.T) {
	s := New()
	s.Open("p1")
	if !s.SetHover(3) {
		t.Fatal("SetHover should report a change")
	}
	if s.Hover() != 3 {
		t.Fatalf("Hover = %d, want 3", s.Hover())
	}
	s.Open("p1") // reopening the same pane still clears hover
	if s.Hover() != -1 {
		t.Fatalf("Open must reset Hover to -1, got %d", s.Hover())
	}
}

// Close must clear the remembered pane so a later OpenOn cannot resolve true off
// a stale id — the core of the "menu shows on the wrong pane" class.
func TestCloseClearsPane(t *testing.T) {
	s := New()
	s.Open("p1")
	s.Close()
	if s.IsOpen() {
		t.Fatal("Close must close the menu")
	}
	if s.OpenOn("p1") {
		t.Fatal("a closed menu must not be open on its former pane")
	}
	if got := s.PaneID(); got != "" {
		t.Fatalf("closed PaneID = %q, want empty", got)
	}
}

func TestToggleSamePaneClosesThenOpens(t *testing.T) {
	s := New()
	if open := s.Toggle("p1"); !open || !s.OpenOn("p1") {
		t.Fatal("first toggle on a pane must open it")
	}
	if open := s.Toggle("p1"); open || s.IsOpen() {
		t.Fatal("second toggle on the same pane must close it")
	}
}

func TestToggleOtherPaneMovesMenu(t *testing.T) {
	s := New()
	s.Toggle("p1")
	if open := s.Toggle("p2"); !open || !s.OpenOn("p2") || s.OpenOn("p1") {
		t.Fatal("toggling a different pane must move the menu to it")
	}
}

// SyncFocus is the "menu only on the focused pane" rule: focus moving away from
// the menu's pane closes it. This is the regression guard for the owner's
// "menu circle still visible on url panes when not focused" report.
func TestSyncFocusClosesOnFocusMoveAway(t *testing.T) {
	s := New()
	s.Open("p1")
	s.SyncFocus("p2") // focus moved to a different pane
	if s.IsOpen() {
		t.Fatal("menu must close when focus leaves its pane")
	}
}

func TestSyncFocusKeepsMenuOnRefocusedPane(t *testing.T) {
	s := New()
	s.Open("p1")
	s.SyncFocus("p1") // focus is (still) on the menu's pane
	if !s.OpenOn("p1") {
		t.Fatal("menu must stay open when its own pane is the focused one")
	}
}

func TestSetHoverNoopWhenClosed(t *testing.T) {
	s := New()
	if s.SetHover(2) {
		t.Fatal("SetHover on a closed menu must be a no-op")
	}
	if s.Hover() != -1 {
		t.Fatalf("closed menu Hover = %d, want -1", s.Hover())
	}
}

func TestSetHoverReportsChangeOnce(t *testing.T) {
	s := New()
	s.Open("p1")
	if !s.SetHover(1) {
		t.Fatal("first SetHover must report a change")
	}
	if s.SetHover(1) {
		t.Fatal("SetHover to the same value must report no change")
	}
}

// The descent/ascent round trip: a menu open on a pane is snapshotted (OpenOn),
// closed for the descent, then restored (Open) on ascent — so you return exactly
// as you left. Mirrors enterPlugin/ascendPortal in the wasm client.
func TestDescendAscentRoundTrip(t *testing.T) {
	s := New()
	s.Open("p1")

	snapshot := s.OpenOn("p1") // recorded into pane.Frame.MenuOpen
	s.Close()                  // descent closes the live menu
	if s.IsOpen() {
		t.Fatal("descent must close the live menu")
	}

	if snapshot { // ascent restores from the frame
		s.Open("p1")
	}
	if !s.OpenOn("p1") {
		t.Fatal("ascent must restore the menu exactly as it was")
	}
}

func TestDescendAscentRoundTripClosedStaysClosed(t *testing.T) {
	s := New()
	snapshot := s.OpenOn("p1") // menu was closed
	s.Close()
	if snapshot {
		s.Open("p1")
	}
	if s.IsOpen() {
		t.Fatal("a menu closed before descent must stay closed on ascent")
	}
}

// TransferFocus is the single-call focus-change helper used by every path that
// moves wasm focus (canvas, forwarded right-down, forwarded left-down). These
// tests prove the omission class is unrepresentable: calling TransferFocus from a
// new path automatically closes the menu when focus moves away from it, without
// any extra thought at the call site.

func TestTransferFocusReturnsChangedAndClosesMenu(t *testing.T) {
	s := New()
	s.Open("p1")
	// Focus moves from p1 → p2: menu must close, changed must be true.
	if !s.TransferFocus("p1", "p2") {
		t.Fatal("TransferFocus must report true when focus changed")
	}
	if s.IsOpen() {
		t.Fatal("TransferFocus must close the menu when focus moves away from its pane")
	}
}

func TestTransferFocusNoopWhenFocusUnchanged(t *testing.T) {
	s := New()
	s.Open("p1")
	// Focus stays on p1: menu must remain open, changed must be false.
	if s.TransferFocus("p1", "p1") {
		t.Fatal("TransferFocus must report false when focus did not change")
	}
	if !s.OpenOn("p1") {
		t.Fatal("TransferFocus must leave the menu untouched when focus did not change")
	}
}

func TestTransferFocusMenuClosedNoChange(t *testing.T) {
	s := New()
	// Menu already closed — TransferFocus must still report the focus change
	// correctly, even though there is nothing to close.
	if !s.TransferFocus("p1", "p2") {
		t.Fatal("TransferFocus must still report true when focus changed (menu already closed)")
	}
	if s.IsOpen() {
		t.Fatal("TransferFocus on an already-closed menu must leave it closed")
	}
}

// Regression guard for the latent twin in onForwardedRightDown: the right-button
// press path used to duplicate the focus-transfer block but omit SyncFocus.
// Calling TransferFocus from all paths (canvas + forwarded right + forwarded left)
// means the menu closes whenever focus moves, regardless of which gesture triggered
// the focus change.
func TestTransferFocusForwardedPathClosesMenu(t *testing.T) {
	s := New()
	s.Open("p1") // menu open on the text pane
	// Simulate: a forwarded press lands on the URL pane (p2), focus moves.
	changed := s.TransferFocus("p1", "p2")
	if !changed {
		t.Fatal("forwarded press that changes focus must report changed=true")
	}
	if s.IsOpen() {
		t.Fatal("forwarded press must close the menu on the de-focused pane")
	}
}
