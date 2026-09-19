package golive

import "testing"

// One table for both kinds: a url passes its leaf-link bit, a shell passes
// false, and the unfreeze arm is the same rule underneath.
func TestDecideTable(t *testing.T) {
	cases := []struct {
		name                        string
		canGoLive, frozen, leafLink bool
		want                        Plan
		ok                          bool
	}{
		{"a host that cannot place a view places nothing", false, true, true, Plan{}, false},
		{"a host with shells turned off attaches nothing", false, true, false, Plan{}, false},
		{"plain row goes live as itself", true, false, false, Plan{}, true},
		{"a frozen row is unfrozen by going live", true, true, false, Plan{Unfreeze: true}, true},
		{"a frozen shell is unfrozen by reconnecting", true, true, false, Plan{Unfreeze: true}, true},
		{"a link goes live as its target", true, false, true, Plan{FollowLink: true}, true},
		{"a frozen link does both", true, true, true, Plan{Unfreeze: true, FollowLink: true}, true},
	}
	for _, c := range cases {
		got, ok := Decide(c.canGoLive, c.frozen, c.leafLink)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: = (%+v, %v), want (%+v, %v)", c.name, got, ok, c.want, c.ok)
		}
	}
}
