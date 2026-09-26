package inflight

import (
	"reflect"
	"testing"
)

func TestLatchHoldsUntilCleared(t *testing.T) {
	l := newLatch()
	if l.has("g1") {
		t.Error("a key nobody failed must not be latched")
	}
	l.set("g1")
	if !l.has("g1") {
		t.Error("a verdict must hold, or the renderer re-asks it every frame")
	}
	if l.has("g2") {
		t.Error("one key's verdict says nothing about another's")
	}
	l.clear("g1")
	if l.has("g1") {
		t.Error("a cleared key must be askable again")
	}
}

func TestLatchClearIfIsScoped(t *testing.T) {
	l := newLatch()
	l.set("a/g1")
	l.set("b/g2")
	l.clearIf(func(k string) bool { return k == "a/g1" })
	if l.has("a/g1") {
		t.Error("the named source's link came back, so its verdicts go")
	}
	if !l.has("b/g2") {
		t.Error("a source going dark must not clear another source's verdicts")
	}
}

func TestLatchResetAndKeys(t *testing.T) {
	l := newLatch()
	l.set("g2")
	l.set("g1")
	if got := l.keys(); !reflect.DeepEqual(got, []string{"g1", "g2"}) {
		t.Errorf("Keys() = %v, want the latched keys sorted", got)
	}
	l.reset()
	if got := l.keys(); len(got) != 0 {
		t.Errorf("Keys() after Reset = %v, want none", got)
	}
}
