//go:build !unix

package shelldriver

import (
	"errors"
	"strings"
	"testing"
)

// Pins that the refusal is classifiable through ErrShellsUnavailable, carries
// a reason a person can read, and opens no half-made PTY.
func TestStartRefusesWithoutAPTY(t *testing.T) {
	s, err := Start(Config{Cols: 80, Rows: 24})
	if err == nil {
		t.Fatal("Start succeeded on a platform with no PTY")
	}
	if s != nil {
		t.Fatalf("Start returned a PTY with its error: %#v", s)
	}
	if !errors.Is(err, ErrShellsUnavailable) {
		t.Errorf("Start error = %v; want it to wrap ErrShellsUnavailable", err)
	}
	if !strings.Contains(err.Error(), "unavailable") {
		t.Errorf("Start error = %q; the user-facing reason must read as unavailability", err)
	}
}

func TestStartRefusesRegardlessOfConfig(t *testing.T) {
	for _, cfg := range []Config{{}, {Cols: 1, Rows: 1}, {Cols: 200, Rows: 60, BashPath: "bash"}} {
		if _, err := Start(cfg); !errors.Is(err, ErrShellsUnavailable) {
			t.Errorf("Start(%+v) error = %v; want ErrShellsUnavailable", cfg, err)
		}
	}
}
