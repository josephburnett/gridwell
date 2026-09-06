package barslot

import "testing"

// Every arm of the slot, and the priority between them. The drawer and the
// click dispatcher both read this one answer, so a row here pins the drawn
// affordance and the click verdict together.
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
			Input{Descent: true, URLDescent: true, URLLive: true, CanLiveURL: true},
			ModeURLBack,
		},
		{
			"a live url descent goes back even where the host says it cannot go live",
			Input{Descent: true, URLDescent: true, URLLive: true},
			ModeURLBack,
		},
		{
			"a frozen url descent on a live-capable host goes live",
			Input{Descent: true, URLDescent: true, CanLiveURL: true},
			ModeURLGoLive,
		},
		{
			"a frozen url descent on a browser host opens a new tab",
			Input{Descent: true, URLDescent: true},
			ModeURLOpenTab,
		},
		{
			"a frozen shell whose refresh shows refreshes",
			Input{Descent: true, ShellDescent: true, ShellRefreshVisible: true},
			ModeShellRefresh,
		},
		{
			"a frozen shell whose session is gone shows nothing",
			Input{Descent: true, ShellDescent: true},
			ModeNothing,
		},
		{
			"a live shell shows nothing",
			Input{Descent: true, ShellDescent: true, ShellLive: true, ShellRefreshVisible: true},
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
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Decide(c.in); got != c.want {
				t.Fatalf("Decide(%+v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// The url arm wins over the shell arm. No real pane is both — a shell descent
// resolves the pane's own grid row and a shell tile is not web content — but
// the drawer used to test url first and the click dispatcher shell first, so
// the priority is pinned here instead of living twice in the shim.
func TestDecideURLBeatsShell(t *testing.T) {
	in := Input{
		Descent:             true,
		URLDescent:          true,
		ShellDescent:        true,
		CanLiveURL:          true,
		ShellRefreshVisible: true,
	}
	if got := Decide(in); got != ModeURLGoLive {
		t.Fatalf("Decide(url and shell) = %v, want %v", got, ModeURLGoLive)
	}
}

// A pane with no descent is the + menu no matter what the stale url and shell
// facts say: Descent is the outer gate, so a leftover fact from the level
// below can never turn a grid's slot into a url button.
func TestDecideGridIgnoresDescentFacts(t *testing.T) {
	in := Input{
		URLDescent:          true,
		URLLive:             true,
		ShellDescent:        true,
		ShellRefreshVisible: true,
		CanLiveURL:          true,
	}
	if got := Decide(in); got != ModePlus {
		t.Fatalf("Decide(no descent) = %v, want %v", got, ModePlus)
	}
}
