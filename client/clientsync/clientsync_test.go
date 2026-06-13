package clientsync

import (
	"errors"
	"testing"
)

func TestClassify(t *testing.T) {
	someErr := errors.New("boom")
	cases := []struct {
		name     string
		err      error
		conflict bool
		want     Reaction
	}{
		{"success", nil, false, Reaction{}},
		// conflict flag is irrelevant when err is nil — still success.
		{"success ignores conflict flag", nil, true, Reaction{}},
		{"conflict refetches, never logs", someErr, true, Reaction{Refetch: true}},
		{"real error logs, never refetches", someErr, false, Reaction{Log: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Classify(c.err, c.conflict)
			if got != c.want {
				t.Errorf("Classify(%v, %v) = %+v, want %+v", c.err, c.conflict, got, c.want)
			}
		})
	}
}

// TestClassifyMutuallyExclusive guards the invariant that Refetch and Log are
// never both set — a reaction is exactly one of success / resync / surface.
func TestClassifyMutuallyExclusive(t *testing.T) {
	for _, conflict := range []bool{true, false} {
		for _, err := range []error{nil, errors.New("x")} {
			r := Classify(err, conflict)
			if r.Refetch && r.Log {
				t.Errorf("Classify(%v,%v) set both Refetch and Log", err, conflict)
			}
		}
	}
}
