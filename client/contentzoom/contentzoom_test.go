package contentzoom

import (
	"math"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

func TestOf(t *testing.T) {
	for _, c := range []struct{ in, want float64 }{{0, 1}, {-1, 1}, {0.5, 0.5}, {2, 2}} {
		if got := Of(c.in); got != c.want {
			t.Errorf("Of(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestClamp(t *testing.T) {
	for _, c := range []struct{ in, want float64 }{
		{0.1, Min}, {Min, Min}, {1, 1}, {Max, Max}, {9, Max},
	} {
		if got := Clamp(c.in); got != c.want {
			t.Errorf("Clamp(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestShellFontPx(t *testing.T) {
	for _, c := range []struct {
		z    float64
		want int
	}{{1, 13}, {2, 26}, {Min, 7}} {
		if got := ShellFontPx(c.z); got != c.want {
			t.Errorf("ShellFontPx(%v) = %v, want %v", c.z, got, c.want)
		}
	}
}

func TestDecide(t *testing.T) {
	cases := []struct {
		name              string
		kind              string
		pageContent       bool
		possiblyEphemeral bool
		key               string
		cur               float64
		want              Verdict
	}{
		{"text in", rpc.KindText, false, false, KeyIn, 1, Verdict{Next: Step, Consume: true, Apply: true, Persist: true}},
		{"text in unshifted", rpc.KindText, false, false, KeyInAlt, 1, Verdict{Next: Step, Consume: true, Apply: true, Persist: true}},
		{"text out", rpc.KindText, false, false, KeyOut, 1, Verdict{Next: 1 / Step, Consume: true, Apply: true, Persist: true}},
		{"text reset", rpc.KindText, false, false, KeyReset, 2.5, Verdict{Next: 1, Consume: true, Apply: true, Persist: true}},
		{"url in", rpc.KindURL, false, false, KeyIn, 1, Verdict{Next: Step, Consume: true, Apply: true, Persist: true}},
		{"shell in", rpc.KindShell, false, false, KeyIn, 1, Verdict{Next: Step, Consume: true, Apply: true, Persist: true}},
		{"zero reads as unzoomed by Of, not here", rpc.KindText, false, false, KeyIn, Of(0), Verdict{Next: Step, Consume: true, Apply: true, Persist: true}},
		{"in stops at Max", rpc.KindText, false, false, KeyIn, Max, Verdict{Next: Max, Consume: true, Apply: true, Persist: true}},
		{"out stops at Min", rpc.KindText, false, false, KeyOut, Min, Verdict{Next: Min, Consume: true, Apply: true, Persist: true}},
		{"ephemeral applies without a write", rpc.KindURL, false, true, KeyIn, 1, Verdict{Next: Step, Consume: true, Apply: true, Persist: false}},
		{"serves_page consumes and does nothing", rpc.KindText, true, false, KeyIn, 1, Verdict{Consume: true}},
		{"serves_page, ephemeral too", rpc.KindText, true, true, KeyOut, 1, Verdict{Consume: true}},
		{"a well is not zoomable", rpc.KindWell, false, false, KeyIn, 1, Verdict{}},
		{"a pane tile is not zoomable", rpc.KindPane, false, false, KeyIn, 1, Verdict{}},
		{"an unknown kind is not zoomable", "sprocket", false, false, KeyIn, 1, Verdict{}},
		{"a key outside the chord is not ours", rpc.KindText, false, false, "z", 1, Verdict{}},
		{"no key is not ours", rpc.KindText, false, false, "", 1, Verdict{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Decide(c.kind, c.pageContent, c.possiblyEphemeral, c.key, c.cur)
			if got.Consume != c.want.Consume || got.Apply != c.want.Apply || got.Persist != c.want.Persist ||
				math.Abs(got.Next-c.want.Next) > 1e-9 {
				t.Errorf("Decide(%q, page=%v, eph=%v, %q, %v) = %+v, want %+v",
					c.kind, c.pageContent, c.possiblyEphemeral, c.key, c.cur, got, c.want)
			}
		})
	}
}

// Every chord key decides, and nothing else does.
func TestKeysAreTheChord(t *testing.T) {
	for _, k := range []string{KeyIn, KeyInAlt, KeyOut, KeyReset} {
		if v := Decide(rpc.KindText, false, false, k, 1); !v.Consume {
			t.Errorf("chord key %q is not consumed by Decide", k)
		}
	}
	for _, k := range []string{"1", "+ ", "=+", "z", "Enter", ""} {
		if v := Decide(rpc.KindText, false, false, k, 1); v.Consume {
			t.Errorf("Decide consumed %q, which is not a chord key", k)
		}
	}
}
