package textedit

import (
	"sync"
	"testing"
	"time"
)

// Pipelined saves for one tile run strictly one after another, so version
// reads at send time chain. Different tiles never block each other.
func TestSaveQueueSerializesPerKey(t *testing.T) {
	q := NewSaveQueue()
	var mu sync.Mutex
	var order []string
	var inFlight, maxInFlight int
	record := func(name string, d time.Duration) func() {
		return func() {
			mu.Lock()
			inFlight++
			if inFlight > maxInFlight {
				maxInFlight = inFlight
			}
			mu.Unlock()
			time.Sleep(d)
			mu.Lock()
			order = append(order, name)
			inFlight--
			mu.Unlock()
		}
	}
	done := make(chan struct{})
	q.Enqueue("tile1", record("a", 30*time.Millisecond))
	q.Enqueue("tile1", record("b", 10*time.Millisecond))
	q.Enqueue("tile1", func() { record("c", 0)(); close(done) })
	<-done
	mu.Lock()
	defer mu.Unlock()
	if maxInFlight != 1 {
		t.Errorf("same-key tasks overlapped: max in flight %d", maxInFlight)
	}
	if len(order) != 3 || order[0] != "a" || order[1] != "b" || order[2] != "c" {
		t.Errorf("order = %v, want strict FIFO a,b,c", order)
	}
}

func TestSaveQueueKeysAreIndependent(t *testing.T) {
	q := NewSaveQueue()
	slowStarted := make(chan struct{})
	fastDone := make(chan struct{})
	q.Enqueue("slow", func() { close(slowStarted); time.Sleep(200 * time.Millisecond) })
	<-slowStarted
	q.Enqueue("fast", func() { close(fastDone) })
	select {
	case <-fastDone:
	case <-time.After(150 * time.Millisecond):
		t.Fatal("a different key was blocked behind the slow one")
	}
}
