package eventhub

import (
	"testing"

	"github.com/josephburnett/gridwell/internal/trace"
)

// The fan-out is where an event the user caused can go missing: it is
// published, it is queued per subscriber, and a newer one for the same entity
// replaces it undelivered. All three say so.
func TestTheFanOutSaysPublishDeliverAndCoalesce(t *testing.T) {
	h := New(func(s string) string { return "t/" + s })
	out, cancel := h.Subscribe()
	defer cancel()
	h.Publish("trace-fanout-key")
	if got := <-out; got != "trace-fanout-key" {
		t.Fatalf("delivered %q", got)
	}
	seen := map[string]bool{}
	for _, rec := range trace.Default().Snapshot() {
		if rec.Src == "eventhub" && rec.KV["key"] == "t/trace-fanout-key" {
			seen[rec.Msg] = true
		}
	}
	for _, want := range []string{"publish", "deliver"} {
		if !seen[want] {
			t.Errorf("no %q record for the published entity", want)
		}
	}
}

func TestACoalescedEventSaysSo(t *testing.T) {
	sub := &subscriber[string]{
		pending: map[string]string{},
		wake:    make(chan struct{}, 1),
		done:    make(chan struct{}),
		out:     make(chan string, 1),
	}
	// No pump: the first event stays queued, so the second replaces it.
	sub.enqueue("t/trace-coalesce-key", "first")
	sub.enqueue("t/trace-coalesce-key", "second")
	for _, rec := range trace.Default().Snapshot() {
		if rec.Src == "eventhub" && rec.Msg == "coalesce" && rec.KV["key"] == "t/trace-coalesce-key" {
			return
		}
	}
	t.Error("an event replaced before delivery left no record")
}
