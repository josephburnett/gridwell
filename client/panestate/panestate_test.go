package panestate

import "testing"

// The package is one plain-data field: what a pane has selected.
func TestNewIsEmpty(t *testing.T) {
	if s := New(); s.Selected != "" {
		t.Fatalf("fresh state = %+v, want empty selection", s)
	}
}
