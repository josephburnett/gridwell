package inflight

import (
	"reflect"
	"testing"
)

func TestLatchHoldsUntilCleared(t *testing.T) {
	l := NewLatch()
	if l.Has("g1") {
		t.Error("a key nobody failed must not be latched")
	}
	l.Set("g1")
	if !l.Has("g1") {
		t.Error("a verdict must hold, or the renderer re-asks it every frame")
	}
	if l.Has("g2") {
		t.Error("one key's verdict says nothing about another's")
	}
	l.Clear("g1")
	if l.Has("g1") {
		t.Error("a cleared key must be askable again")
	}
}

func TestLatchClearIfIsScoped(t *testing.T) {
	l := NewLatch()
	l.Set("a/g1")
	l.Set("b/g2")
	l.ClearIf(func(k string) bool { return k == "a/g1" })
	if l.Has("a/g1") {
		t.Error("the named source's link came back, so its verdicts go")
	}
	if !l.Has("b/g2") {
		t.Error("a source going dark must not clear another source's verdicts")
	}
}

func TestLatchResetAndKeys(t *testing.T) {
	l := NewLatch()
	l.Set("g2")
	l.Set("g1")
	if got := l.Keys(); !reflect.DeepEqual(got, []string{"g1", "g2"}) {
		t.Errorf("Keys() = %v, want the latched keys sorted", got)
	}
	l.Reset()
	if got := l.Keys(); len(got) != 0 {
		t.Errorf("Keys() after Reset = %v, want none", got)
	}
}
