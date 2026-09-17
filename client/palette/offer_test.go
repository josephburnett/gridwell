package palette

import "testing"

func TestOffer(t *testing.T) {
	cases := []struct {
		name              string
		in                Offer
		primitives, shell bool
	}{
		{"writable node with shells", Offer{Writable: true}, true, true},
		{"writable node, shells disabled", Offer{Writable: true, ShellsDisabled: true}, true, false},
		{"read-only grid offers nothing, shells or not", Offer{}, false, false},
		{"read-only grid on a shell-less node", Offer{ShellsDisabled: true}, false, false},
	}
	for _, c := range cases {
		if got := c.in.Primitives(); got != c.primitives {
			t.Errorf("%s: Primitives = %v, want %v", c.name, got, c.primitives)
		}
		if got := c.in.Shell(); got != c.shell {
			t.Errorf("%s: Shell = %v, want %v", c.name, got, c.shell)
		}
	}
}
