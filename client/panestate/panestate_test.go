package panestate

import "testing"

// The package is now one plain-data field: what a pane has selected. The
// ascent stack it used to own is the pane's own place stack (client/pane),
// and the framing-writer rule is pane.FramingWriters — both moved so the
// "where am I" facts have ONE owner (S8).
func TestNewIsEmpty(t *testing.T) {
	if s := New(); s.Selected != "" {
		t.Fatalf("fresh state = %+v, want empty selection", s)
	}
}
