package inflight

import (
	"sync"
	"testing"
)

func TestWritesLenIsDispatchedMinusSettled(t *testing.T) {
	var w Writes
	if w.Any() || w.Len() != 0 {
		t.Fatalf("a fresh tally counts %d writes, want 0", w.Len())
	}
	w.Start()
	if !w.Any() || w.Len() != 1 {
		t.Fatalf("after one dispatch Len is %d, want 1", w.Len())
	}
	w.Start()
	w.Done()
	if !w.Any() || w.Len() != 1 {
		t.Fatalf("two dispatches and one settle leave %d, want 1", w.Len())
	}
	w.Done()
	if w.Any() || w.Len() != 0 {
		t.Fatalf("a settled pair leaves %d in flight, want 0", w.Len())
	}
}

// A write settles on the goroutine that ran it, never the one that posted it.
func TestWritesSettleFromAnotherGoroutine(t *testing.T) {
	var w Writes
	var wg sync.WaitGroup
	for range 100 {
		w.Start()
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.Done()
		}()
	}
	wg.Wait()
	if w.Any() {
		t.Fatalf("%d writes left in flight after every one settled", w.Len())
	}
}
