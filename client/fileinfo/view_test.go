package fileinfo

import "testing"

// TestAnswerLandsOnlyInTheViewItWasAskedFor plays a reset between a read's
// ask and its answer, for the info read and for a hex page: an answer from an
// earlier view lands nowhere, even when the view shows the same file again.
func TestAnswerLandsOnlyInTheViewItWasAskedFor(t *testing.T) {
	cases := []struct {
		name  string
		after []string // the id and stamp of each Show between ask and answer
		lands bool
	}{
		{"nothing moved", nil, true},
		{"the same file shown again", []string{"F", "1"}, true},
		{"info read, the stamp moved", []string{"F", "2"}, false},
		{"hex page, another file and back", []string{"G", "1", "F", "1"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var v View
			if !v.Show("F", "1") {
				t.Fatal("the first file shown is a reset")
			}
			asked := v.Ask()
			for i := 0; i < len(c.after); i += 2 {
				v.Show(c.after[i], c.after[i+1])
			}
			if got := v.Lands(asked); got != c.lands {
				t.Errorf("Lands = %v, want %v", got, c.lands)
			}
		})
	}
}
