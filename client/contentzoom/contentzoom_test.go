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
		{0.1, rpc.ContentZoomMin}, {rpc.ContentZoomMin, rpc.ContentZoomMin}, {1, 1}, {rpc.ContentZoomMax, rpc.ContentZoomMax}, {9, rpc.ContentZoomMax},
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
	}{{1, 13}, {2, 26}, {rpc.ContentZoomMin, 7}} {
		if got := ShellFontPx(c.z); got != c.want {
			t.Errorf("ShellFontPx(%v) = %v, want %v", c.z, got, c.want)
		}
	}
}

func TestDecide(t *testing.T) {
	cases := []struct {
		name string
		kind string
		key  string
		cur  float64
		want Verdict
	}{
		{"text in", rpc.KindText, KeyIn, 1, Verdict{Next: Step, Consume: true, Apply: true}},
		{"text in unshifted", rpc.KindText, KeyInAlt, 1, Verdict{Next: Step, Consume: true, Apply: true}},
		{"text out", rpc.KindText, KeyOut, 1, Verdict{Next: 1 / Step, Consume: true, Apply: true}},
		{"text reset", rpc.KindText, KeyReset, 2.5, Verdict{Next: 1, Consume: true, Apply: true}},
		{"url in", rpc.KindURL, KeyIn, 1, Verdict{Next: Step, Consume: true, Apply: true}},
		{"shell in", rpc.KindShell, KeyIn, 1, Verdict{Next: Step, Consume: true, Apply: true}},
		{"zero reads as unzoomed by Of, not here", rpc.KindText, KeyIn, Of(0), Verdict{Next: Step, Consume: true, Apply: true}},
		{"in stops at Max", rpc.KindText, KeyIn, rpc.ContentZoomMax, Verdict{Next: rpc.ContentZoomMax, Consume: true, Apply: true}},
		{"out stops at Min", rpc.KindText, KeyOut, rpc.ContentZoomMin, Verdict{Next: rpc.ContentZoomMin, Consume: true, Apply: true}},
		{"a door is not zoomable", rpc.KindDoor, KeyIn, 1, Verdict{}},
		{"a workspace tile is not zoomable", rpc.KindWorkspace, KeyIn, 1, Verdict{}},
		{"an unknown kind is not zoomable", "sprocket", KeyIn, 1, Verdict{}},
		{"a key outside the chord is not ours", rpc.KindText, "z", 1, Verdict{}},
		{"no key is not ours", rpc.KindText, "", 1, Verdict{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Decide(c.kind, c.key, c.cur)
			if got.Consume != c.want.Consume || got.Apply != c.want.Apply ||
				math.Abs(got.Next-c.want.Next) > 1e-9 {
				t.Errorf("Decide(%q, %q, %v) = %+v, want %+v",
					c.kind, c.key, c.cur, got, c.want)
			}
		})
	}
}

// Every zoom a press shows is one the writers accept, so its persist is never
// refused.
func TestEveryPressIsWritable(t *testing.T) {
	for _, k := range []string{KeyIn, KeyOut, KeyReset} {
		for cur := 0.1; cur < 10; cur *= 1.07 {
			v := Decide(rpc.KindText, k, cur)
			if _, err := rpc.NewContentZoom(v.Next); err != nil {
				t.Errorf("press %q at %v shows %v: %v", k, cur, v.Next, err)
			}
		}
	}
}

// Every chord key decides, and nothing else does.
func TestKeysAreTheChord(t *testing.T) {
	for _, k := range []string{KeyIn, KeyInAlt, KeyOut, KeyReset} {
		if v := Decide(rpc.KindText, k, 1); !v.Consume {
			t.Errorf("chord key %q is not consumed by Decide", k)
		}
	}
	for _, k := range []string{"1", "+ ", "=+", "z", "Enter", ""} {
		if v := Decide(rpc.KindText, k, 1); v.Consume {
			t.Errorf("Decide consumed %q, which is not a chord key", k)
		}
	}
}
