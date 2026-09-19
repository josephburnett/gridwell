package urlview

import "testing"

func TestDecideGoLiveTable(t *testing.T) {
	cases := []struct {
		name                      string
		liveURL, frozen, leafLink bool
		want                      GoLive
		ok                        bool
	}{
		{"browser host places nothing", false, true, true, GoLive{}, false},
		{"plain row goes live as itself", true, false, false, GoLive{}, true},
		{"a frozen row is unfrozen by going live", true, true, false, GoLive{Unfreeze: true}, true},
		{"a link goes live as its target", true, false, true, GoLive{FollowLink: true}, true},
		{"a frozen link does both", true, true, true, GoLive{Unfreeze: true, FollowLink: true}, true},
	}
	for _, c := range cases {
		got, ok := DecideGoLive(c.liveURL, c.frozen, c.leafLink)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: = (%+v, %v), want (%+v, %v)", c.name, got, ok, c.want, c.ok)
		}
	}
}
