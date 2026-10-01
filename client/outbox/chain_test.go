package outbox

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
	record := func(name string, d time.Duration) func() bool {
		return func() bool {
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
			return true
		}
	}
	done := make(chan struct{})
	q.Enqueue("tile1", record("a", 30*time.Millisecond))
	q.Enqueue("tile1", record("b", 10*time.Millisecond))
	q.Enqueue("tile1", func() bool { record("c", 0)(); close(done); return true })
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
	q.Enqueue("slow", func() bool { close(slowStarted); time.Sleep(200 * time.Millisecond); return true })
	<-slowStarted
	q.Enqueue("fast", func() bool { close(fastDone); return true })
	select {
	case <-fastDone:
	case <-time.After(150 * time.Millisecond):
		t.Fatal("a different key was blocked behind the slow one")
	}
}

// node is a last-writer-wins store: what a pane layout's WriteContent is.
type node struct {
	mu   sync.Mutex
	held string
}

func (n *node) write(layout string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.held = layout
}

func (n *node) read() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.held
}

// A debounced layout save still on the wire when the flush save is made must
// land first, so the flush's held callback sees the node hold the flushed
// layout and nothing older lands after it.
func TestSaveQueueLayoutFlushObservesTheFinalLayout(t *testing.T) {
	q := NewSaveQueue()
	n := &node{}
	release := make(chan struct{})
	q.Enqueue("pt1", func() bool {
		<-release
		n.write("A: names the visit")
		return true
	})
	observed := make(chan string, 1)
	q.Enqueue("pt1", func() bool {
		n.write("B: visit gone")
		observed <- n.read()
		return true
	})
	close(release)
	if got := <-observed; got != "B: visit gone" {
		t.Errorf("the flush heard held while the node held %q", got)
	}
	idle := make(chan string, 1)
	q.After("pt1", func(bool) { idle <- n.read() })
	if got := <-idle; got != "B: visit gone" {
		t.Errorf("final layout = %q, want the flushed one", got)
	}
}

// A flush with nothing new to write still waits for the write on the wire and
// hears its verdict, because that write is what the node will hold.
func TestSaveQueueAfterHearsTheLastWrite(t *testing.T) {
	q := NewSaveQueue()
	got := make(chan bool, 1)
	q.After("idle", func(landed bool) { got <- landed })
	if !<-got {
		t.Error("an idle chain owes nothing and must answer true")
	}

	n := &node{}
	release := make(chan struct{})
	q.Enqueue("pt1", func() bool {
		<-release
		n.write("Y")
		return false
	})
	heard := make(chan string, 1)
	q.After("pt1", func(landed bool) {
		got <- landed
		heard <- n.read()
	})
	select {
	case <-heard:
		t.Fatal("After answered while a write was still on the wire")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if <-got {
		t.Error("After reported landed for a write that did not land")
	}
	if h := <-heard; h != "Y" {
		t.Errorf("After ran before the write finished: node held %q", h)
	}
}
