package events

import (
	"cmp"
	"slices"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/client/cadence"
)

// clock is a debounce Schedule and Clock over virtual milliseconds.
type clock struct {
	now  float64
	dues []float64
	runs []func()
}

func (c *clock) schedule(ms int, fire func()) {
	c.dues = append(c.dues, c.now+float64(ms))
	c.runs = append(c.runs, fire)
}

func (c *clock) read() float64 { return c.now }

// to moves the clock to t and runs what came due, earliest first.
func (c *clock) to(t float64) {
	c.now = t
	for {
		next := -1
		for i, due := range c.dues {
			if due <= c.now && (next < 0 || due < c.dues[next]) {
				next = i
			}
		}
		if next < 0 {
			return
		}
		fire := c.runs[next]
		c.dues = append(c.dues[:next], c.dues[next+1:]...)
		c.runs = append(c.runs[:next], c.runs[next+1:]...)
		fire()
	}
}

type flip struct {
	at      float64
	source  string
	healthy bool
}

// flaps alternates n flips of source, gap ms apart, ending on last.
func flaps(source string, n int, gap float64, last bool) []flip {
	out := make([]flip, n)
	for i := range out {
		out[i] = flip{at: float64(i) * gap, source: source, healthy: last == ((n-1-i)%2 == 0)}
	}
	return out
}

// A health transition resyncs its source once the health has held for
// cadence.HealthSettleMs, so a burst of flips costs one resync of the final
// state rather than one per flip; the notice is ReactHealth's, per flip, and
// ends on the last one.
func TestAHealthBurstResyncsOnceItSettles(t *testing.T) {
	const settle = cadence.HealthSettleMs
	cases := []struct {
		name    string
		flips   []flip
		resyncs map[string]int
		// notice is whether each source ends with its sticky notice up.
		notice map[string]bool
	}{
		{"a source that comes up resyncs once",
			[]flip{{0, "n/c", true}}, map[string]int{"n/c": 1}, map[string]bool{"n/c": false}},
		{"a source that goes down resyncs once and says so",
			[]flip{{0, "n/c", false}}, map[string]int{"n/c": 1}, map[string]bool{"n/c": true}},
		{"twenty flips in a second ending down resync once",
			flaps("n/c", 20, 50, false), map[string]int{"n/c": 1}, map[string]bool{"n/c": true}},
		{"twenty flips in a second ending up resync once",
			flaps("n/c", 20, 50, true), map[string]int{"n/c": 1}, map[string]bool{"n/c": false}},
		{"a flap that rests between flips resyncs each rest",
			[]flip{{0, "n/c", false}, {2 * settle, "n/c", true}}, map[string]int{"n/c": 2}, map[string]bool{"n/c": false}},
		{"one source's flap does not hold another's resync",
			append(flaps("n/a", 10, settle/2, true), flip{0, "n/b", false}),
			map[string]int{"n/a": 1, "n/b": 1}, map[string]bool{"n/a": false, "n/b": true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clk := &clock{}
			got := map[string]int{}
			r := NewResyncs(clk.schedule, clk.read, func(source string) { got[source]++ })
			notice := map[string]bool{}
			lastFlip := map[string]float64{}
			last := 0.0
			slices.SortStableFunc(c.flips, func(a, b flip) int { return cmp.Compare(a.at, b.at) })
			for _, f := range c.flips {
				clk.to(f.at)
				h := ReactHealth(&pb.EventPluginHealth{PluginUuid: f.source, Healthy: f.healthy})
				notice[f.source] = h.Report
				r.Transition(h.Resync)
				lastFlip[f.source] = f.at
				last = max(last, f.at)
			}
			clk.to(last + settle - 1)
			for src, at := range lastFlip {
				if at == last && got[src] >= c.resyncs[src] {
					t.Errorf("%s resynced %d times before its health held for %dms", src, got[src], settle)
				}
			}
			clk.to(last + 10*settle)
			for src, want := range c.resyncs {
				if got[src] != want {
					t.Errorf("%s resynced %d times, want %d", src, got[src], want)
				}
			}
			for src, want := range c.notice {
				if notice[src] != want {
					t.Errorf("%s notice up = %v, want %v", src, notice[src], want)
				}
			}
		})
	}
}
