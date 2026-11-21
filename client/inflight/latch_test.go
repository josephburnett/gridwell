package inflight

import (
	"reflect"
	"testing"
)

func TestLatchHoldsUntilCleared(t *testing.T) {
	r := NewReads()
	if r.Failed("g1") {
		t.Error("a key nobody failed must not be latched")
	}
	r.Settle("g1", Refused)
	if !r.Failed("g1") {
		t.Error("a verdict must hold, or the renderer re-asks it every draw")
	}
	if r.Failed("g2") {
		t.Error("one key's verdict says nothing about another's")
	}
	r.Settle("g1", Answered)
	if r.Failed("g1") {
		t.Error("a cleared key must be askable again")
	}
}

func TestLatchClearIfIsScoped(t *testing.T) {
	r := NewReads()
	r.Settle("a/g1", Refused)
	r.Settle("b/g2", Refused)
	r.ClearIf(func(k string) bool { return k == "a/g1" })
	if r.Failed("a/g1") {
		t.Error("the named source's link came back, so its verdicts go")
	}
	if !r.Failed("b/g2") {
		t.Error("a source going dark must not clear another source's verdicts")
	}
}

func TestLatchResetAndKeys(t *testing.T) {
	r := NewReads()
	r.Settle("g2", Unreachable)
	r.Settle("g1", Dead)
	if got := r.FailedKeys(); !reflect.DeepEqual(got, []string{"g1", "g2"}) {
		t.Errorf("FailedKeys() = %v, want the latched keys sorted", got)
	}
	r.Reset()
	if got := r.FailedKeys(); len(got) != 0 {
		t.Errorf("FailedKeys() after Reset = %v, want none", got)
	}
}
