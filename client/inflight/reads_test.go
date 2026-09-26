package inflight

import (
	"slices"
	"strings"
	"testing"
)

// The table a per-frame read runs on: which signal clears which latch. A
// latch nothing clears is a face that never loads; a failure nothing latches
// is a read per frame.
func TestReadsLatchTable(t *testing.T) {
	type signal struct {
		name  string
		apply func(r *Reads, key string)
	}
	signals := []signal{
		{"answer", func(r *Reads, k string) { r.Settle(k, Answered) }},
		{"change", func(r *Reads, k string) { r.Change(k) }},
		{"backstop", func(r *Reads, _ string) { r.Backstop() }},
		{"clearIf", func(r *Reads, _ string) { r.ClearIf(func(string) bool { return true }) }},
		{"reset", func(r *Reads, _ string) { r.Reset() }},
	}
	clears := map[Verdict]map[string]bool{
		Refused:     {"answer": true, "change": true, "backstop": false, "clearIf": true, "reset": true},
		Unreachable: {"answer": true, "change": true, "backstop": true, "clearIf": true, "reset": true},
	}
	for v, want := range clears {
		for _, s := range signals {
			r := NewReads()
			r.Settle("g", v)
			if _, _, ok := r.Ask("g"); ok {
				t.Fatalf("verdict %v: a latched key was asked", v)
			}
			s.apply(r, "g")
			_, done, ok := r.Ask("g")
			if ok != want[s.name] {
				t.Errorf("verdict %v, %s: asks again = %v, want %v", v, s.name, ok, want[s.name])
			}
			if ok {
				done()
			}
		}
	}
}

func TestReadsAskDedupesAndSettleReplaces(t *testing.T) {
	r := NewReads()
	_, done, ok := r.Ask("c")
	if !ok {
		t.Fatal("a fresh key was refused")
	}
	if _, _, again := r.Ask("c"); again {
		t.Error("a key in flight was asked twice")
	}
	if !slices.Equal(r.InFlight(), []string{"c"}) {
		t.Errorf("in flight: %v", r.InFlight())
	}
	done()
	r.Settle("c", Unreachable)
	r.Settle("c", Refused)
	if !r.Refused("c") || !slices.Equal(r.Backstop(), nil) || !r.Failed("c") {
		t.Error("a verdict after an outage is a verdict, and the backstop leaves it")
	}
	r.Settle("c", Unreachable)
	if r.Refused("c") || !r.Failed("c") {
		t.Error("an outage after a verdict is an outage")
	}
}

// Backstop and ClearIf name what they cleared, because nothing draws on a
// tick: the caller re-asks those keys by name.
func TestReadsNameWhatTheyClear(t *testing.T) {
	r := NewReads()
	r.Settle("n/a", Unreachable)
	r.Settle("n/b", Refused)
	r.Settle("m/c", Unreachable)
	if got := r.FailedKeys(); !slices.Equal(got, []string{"m/c", "n/a", "n/b"}) {
		t.Errorf("failed keys: %v", got)
	}
	if got := r.ClearIf(func(k string) bool { return strings.HasPrefix(k, "n/") }); !slices.Equal(got, []string{"n/a", "n/b"}) {
		t.Errorf("clearIf: %v", got)
	}
	if got := r.Backstop(); !slices.Equal(got, []string{"m/c"}) {
		t.Errorf("backstop: %v", got)
	}
	if got := r.FailedKeys(); len(got) != 0 {
		t.Errorf("left latched: %v", got)
	}
}
