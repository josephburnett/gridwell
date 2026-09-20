package pane

import "testing"

func TestNewSessionStateIsEmpty(t *testing.T) {
	if s := NewSessionState(); s.Selected != "" {
		t.Fatalf("fresh state = %+v, want empty selection", s)
	}
}
