package barslot

import (
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

// The drawer and the click dispatcher read the same answer, so a row here pins
// the drawn affordance and the click verdict together.
func TestDecide(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		want Mode
	}{
		{
			"a grid is the + menu",
			Input{},
			ModePlus,
		},
		{
			"a grid on a browser host is still the + menu",
			Input{CanLiveURL: false},
			ModePlus,
		},
		{
			"a live url descent goes back",
			Input{Descent: true, Content: rpc.DescentURL, URLLive: true, CanLiveURL: true},
			ModeURLBack,
		},
		{
			"a live url descent goes back even where the host says it cannot go live",
			Input{Descent: true, Content: rpc.DescentURL, URLLive: true},
			ModeURLBack,
		},
		{
			"a frozen url descent on a live-capable host goes live",
			Input{Descent: true, Content: rpc.DescentURL, CanLiveURL: true},
			ModeGoLive,
		},
		{
			"a frozen url descent on a browser host opens a new tab",
			Input{Descent: true, Content: rpc.DescentURL},
			ModeURLOpenTab,
		},
		{
			"a frozen shell whose refresh shows goes live",
			Input{Descent: true, Content: rpc.DescentShell, Durable: true, ShellRefreshVisible: true},
			ModeGoLive,
		},
		{
			"a frozen shell whose session is gone shows nothing",
			Input{Descent: true, Content: rpc.DescentShell},
			ModeNothing,
		},
		{
			"a live shell freezes",
			Input{Descent: true, Content: rpc.DescentShell, ShellLive: true, Durable: true, ShellRefreshVisible: true},
			ModeFreeze,
		},
		{
			"an ephemeral live shell has no row to freeze onto",
			Input{Descent: true, Content: rpc.DescentShell, ShellLive: true},
			ModeNothing,
		},
		{
			"a markdown descent shows nothing",
			Input{Descent: true},
			ModeNothing,
		},
		{
			"a descent is never the + menu, whatever else is false",
			Input{Descent: true, CanLiveURL: true},
			ModeNothing,
		},
		{
			// The family is the gate, so a surface left live below cannot
			// hand a markdown descent a url or a shell button.
			"a markdown descent ignores every live fact",
			Input{Descent: true, URLLive: true, ShellLive: true,
				Durable: true, ShellRefreshVisible: true, CanLiveURL: true},
			ModeNothing,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Decide(c.in); got != c.want {
				t.Fatalf("Decide(%+v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// Descent is the outer gate, so a leftover fact from the level below cannot
// turn a grid's slot into a url button.
func TestDecideGridIgnoresDescentFacts(t *testing.T) {
	in := Input{
		Content:             rpc.DescentURL,
		URLLive:             true,
		ShellLive:           true,
		Durable:             true,
		ShellRefreshVisible: true,
		CanLiveURL:          true,
	}
	if got := Decide(in); got != ModePlus {
		t.Fatalf("Decide(no descent) = %v, want %v", got, ModePlus)
	}
}

// The e2e asserts the circle by name, so a mode with no name of its own would
// read there as another mode's affordance.
func TestModeNames(t *testing.T) {
	seen := map[string]bool{}
	for m := ModeNothing; m <= ModePlus; m++ {
		name := m.String()
		if name == "" || (name == "nothing" && m != ModeNothing) {
			t.Errorf("mode %d has no name of its own (%q)", int(m), name)
		}
		if seen[name] {
			t.Errorf("mode %d repeats the name %q", int(m), name)
		}
		seen[name] = true
	}
}
