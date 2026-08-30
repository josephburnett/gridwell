package eventhub

import (
	"strconv"
	"testing"
	"time"
)

type ev struct {
	key string
	n   int
}

func drain(t *testing.T, ch <-chan ev, want int) []ev {
	t.Helper()
	var out []ev
	deadline := time.After(3 * time.Second)
	for len(out) < want {
		select {
		case e := <-ch:
			out = append(out, e)
		case <-deadline:
			t.Fatalf("got %d events, want %d", len(out), want)
		}
	}
	select {
	case e := <-ch:
		t.Fatalf("extra event %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
	return out
}

// Distinct entities are never dropped for a stalled subscriber; the same
// entity coalesces to its latest state; an unkeyable event ("") is never
// coalesced.
func TestHubCoalescesPerKeyAndDropsNothing(t *testing.T) {
	h := New(func(e ev) string { return e.key })
	ch, cancel := h.Subscribe()
	defer cancel()

	for i := 0; i < 200; i++ {
		h.Publish(ev{key: "k" + strconv.Itoa(i), n: i})
	}
	got := drain(t, ch, 200)
	for i, e := range got {
		if e.key != "k"+strconv.Itoa(i) {
			t.Fatalf("event %d = %+v; want first-touch order", i, e)
		}
	}

	for i := 0; i < 50; i++ {
		h.Publish(ev{key: "same", n: i})
	}
	// Distinct tiles interleaved: the coalesced entity keeps its first
	// slot, later entities follow.
	h.Publish(ev{key: "other", n: 1})
	h.Publish(ev{key: "same", n: 99})
	got = drain(t, ch, 2)
	if got[0].key != "same" || got[0].n != 99 || got[1].key != "other" {
		t.Fatalf("coalesce: got %+v, want same@99 then other", got)
	}

	for i := 0; i < 3; i++ {
		h.Publish(ev{key: "", n: i})
	}
	got = drain(t, ch, 3)
	for i, e := range got {
		if e.n != i {
			t.Fatalf("unkeyed events must not coalesce: got %+v", got)
		}
	}
}

// Cancel closes the stream, detaches the subscriber, and is safe to call
// twice, as with a deferred cancel after an explicit one.
func TestCancelClosesAndIsIdempotent(t *testing.T) {
	h := New(func(e ev) string { return e.key })
	ch, cancel := h.Subscribe()
	cancel()
	cancel()
	h.Publish(ev{key: "k"})
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("event delivered after cancel")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream not closed after cancel")
	}
	h.mu.Lock()
	n := len(h.subs)
	h.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d subscribers still registered after cancel", n)
	}
}
