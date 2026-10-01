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
		{"revive", func(r *Reads, _ string) { r.ReviveIf(func(string) bool { return true }) }},
	}
	clears := map[Verdict]map[string]bool{
		Refused:     {"answer": true, "change": true, "backstop": false, "clearIf": true, "reset": true, "revive": false},
		Dead:        {"answer": true, "change": true, "backstop": false, "clearIf": true, "reset": true, "revive": true},
		Unreachable: {"answer": true, "change": true, "backstop": true, "clearIf": true, "reset": true, "revive": false},
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

// Dead is a Refused the renderer can tell apart, and it goes with the latch:
// a key the node answered dead is asked again on the same signals, and a
// different answer replaces it.
func TestDeadIsARefusalThatSaysSo(t *testing.T) {
	r := NewReads()
	r.Settle("g", Dead)
	if !r.Dead("g") || !r.Refused("g") {
		t.Fatal("a dead verdict must read dead and latch as refused")
	}
	for _, v := range []Verdict{Answered, Refused, Unreachable} {
		r.Settle("g", Dead)
		r.Settle("g", v)
		if r.Dead("g") {
			t.Errorf("settling %v after Dead left the key dead", v)
		}
	}
	r.Settle("g", Dead)
	r.ClearIf(func(string) bool { return true })
	if r.Dead("g") {
		t.Error("ClearIf left the key dead")
	}
	r.Settle("g", Dead)
	r.Reset()
	if r.Dead("g") {
		t.Error("Reset left the key dead")
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

// ReviveIf names the dead keys it cleared, scoped by match, because the
// namespace that changed is the only one whose gone keys may be back.
func TestReviveNamesTheDeadKeysItClears(t *testing.T) {
	r := NewReads()
	r.Settle("n/a", Dead)
	r.Settle("n/b", Refused)
	r.Settle("m/c", Dead)
	if got := r.ReviveIf(func(k string) bool { return strings.HasPrefix(k, "n/") }); !slices.Equal(got, []string{"n/a"}) {
		t.Errorf("revived %v, want only the dead key in n", got)
	}
	if r.Failed("n/a") || !r.Refused("n/b") || !r.Dead("m/c") {
		t.Error("revive cleared outside the dead keys it matched")
	}
}

// The sequence a dropped refetch loses: a read is in flight, the thing it
// asks about changes, and the ask that change makes is refused. The answer
// already on the wire was taken before the change, so it is owed a re-ask.
func TestAChangeInFlightIsOwedToTheHolder(t *testing.T) {
	r := NewReads()
	server, cache, changed := "v1", "", false
	var fetch func()
	fetch = func() {
		_, done, ok := r.Ask("g1")
		if !ok {
			return
		}
		read := server // the answer leaves the server now
		if !changed {
			// The change lands while this read is on the wire.
			changed, server = true, "v2"
			r.Change("g1")
			fetch()
		}
		cache = read
		if done() {
			fetch()
		}
	}
	fetch()
	if cache != "v2" {
		t.Errorf("cache = %q, want %q: the change that landed mid-flight was dropped", cache, "v2")
	}
}

// A draw asks every frame while a read is in flight. Its ask carries nothing
// the read in flight does not already answer, so it owes no second read.
func TestARepeatedAskOwesNothing(t *testing.T) {
	r := NewReads()
	_, done, ok := r.Ask("g1")
	if !ok {
		t.Fatal("a fresh key was refused")
	}
	for range 3 {
		if _, _, again := r.Ask("g1"); again {
			t.Fatal("a key in flight was asked twice")
		}
	}
	if done() {
		t.Error("a frame's repeated ask owed a re-read: every grid in flight is read twice")
	}
}

// A caller that must have the answer, the restore walk, waits on the read
// already in flight instead of issuing a second one, and the end it waits on
// is the one the read's done closes.
func TestJoinSharesTheReadInFlight(t *testing.T) {
	r := NewReads()
	_, done, end, ok := r.Join("g1")
	if !ok || end == nil {
		t.Fatal("a fresh key must be claimed, with an end to wait on")
	}
	_, _, joined, again := r.Join("g1")
	if again {
		t.Fatal("a key in flight was read twice")
	}
	if joined != end {
		t.Fatal("the joiner must wait on the read in flight")
	}
	select {
	case <-joined:
		t.Fatal("the read has not answered yet")
	default:
	}
	done()
	select {
	case <-joined:
	default:
		t.Fatal("the read answered and the joiner is still waiting")
	}

	r.Settle("g2", Refused)
	if _, _, end, ok := r.Join("g2"); ok || end != nil {
		t.Error("a latched key is neither read nor waited on")
	}
}
