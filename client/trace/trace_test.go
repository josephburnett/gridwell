package trace

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/api/tracewire"
)

var t0 = time.UnixMilli(1727000000123)

// The batch a node reads is pinned to its bytes: JSON lines, one record each,
// newline-terminated, with the fields the door's contract names.
func TestPendingBatchIsJSONLines(t *testing.T) {
	c := New(8, "cid7abc")
	c.Emit("nav", "push", "descend a1b2c3d", map[string]string{"req": "k3f9x2a"}, t0)
	c.Emit("rpc", "call", "GetGrid", nil, t0.Add(2*time.Millisecond))
	batch, _ := c.PendingBatch()
	const want = `{"origin":"client","src":"nav","kind":"push","msg":"descend a1b2c3d","kv":{"req":"k3f9x2a"},"cid":"cid7abc","ct":1727000000123}` + "\n" +
		`{"origin":"client","src":"rpc","kind":"call","msg":"GetGrid","cid":"cid7abc","ct":1727000000125}` + "\n"
	if string(batch) != want {
		t.Errorf("batch is\n%s\nwant\n%s", batch, want)
	}
}

// An address in a message is read by a person, so the encoder's HTML escaping
// is off and the message survives as typed.
func TestMarshalLeavesAMessageAsWritten(t *testing.T) {
	b := Marshal([]tracewire.Record{{Origin: tracewire.OriginClient, Src: "url", Kind: "open", Msg: "https://x.test/a?b=1&c=2"}})
	if !strings.Contains(string(b), "b=1&c=2") {
		t.Errorf("the message was escaped: %s", b)
	}
}

// Nothing to say is nothing to post: an empty batch would cost a round trip
// per tick for the life of the page.
func TestPendingIsEmptyWhenNothingIsOwed(t *testing.T) {
	c := New(4, "cid7abc")
	if b, _ := c.PendingBatch(); len(b) != 0 {
		t.Errorf("a fresh client owes %q", b)
	}
	c.Emit("nav", "push", "x", nil, t0)
	_, ack := c.PendingBatch()
	ack()
	if b, _ := c.PendingBatch(); len(b) != 0 {
		t.Errorf("an acknowledged batch is owed again: %q", b)
	}
	if n := c.PendingCount(); n != 0 {
		t.Errorf("PendingCount = %d after the ack, want 0", n)
	}
}

// A post that never lands must not lose the records it carried: the door is
// unreliable and the trace is what explains the failure.
func TestAFailedPostIsResentWithTheNextBatch(t *testing.T) {
	c := New(8, "cid7abc")
	c.Emit("nav", "push", "one", nil, t0)
	first, _ := c.PendingBatch() // no ack: the post failed
	c.Emit("nav", "push", "two", nil, t0)
	second, _ := c.PendingBatch()
	if lines(t, second) != 2 {
		t.Fatalf("the resend carries %d records, want both:\n%s", lines(t, second), second)
	}
	if !strings.HasPrefix(string(second), string(first)) {
		t.Errorf("the resend does not open with the batch that failed:\n%s", second)
	}
}

// The ack answers one batch, and records emitted while that post was in
// flight were never in it.
func TestAckSparesWhatWasEmittedInFlight(t *testing.T) {
	c := New(8, "cid7abc")
	c.Emit("nav", "push", "one", nil, t0)
	_, ack := c.PendingBatch()
	c.Emit("nav", "push", "two", nil, t0)
	ack()
	if n := c.PendingCount(); n != 1 {
		t.Fatalf("PendingCount = %d, want the in-flight record still owed", n)
	}
	b, _ := c.PendingBatch()
	if !strings.Contains(string(b), `"two"`) || strings.Contains(string(b), `"one"`) {
		t.Errorf("the next batch is\n%s\nwant only the record emitted in flight", b)
	}
}

// The ring is the bound on an unreachable node. What it drops is a hole in the
// story, so the hole is told: one record, with the count.
func TestAWrapDropsTheOldestUnackedAndSaysSo(t *testing.T) {
	c := New(4, "cid7abc")
	for _, m := range []string{"one", "two", "three", "four", "five", "six"} {
		c.Emit("nav", "push", m, nil, t0)
	}
	b, _ := c.PendingBatch()
	if strings.Contains(string(b), `"one"`) || strings.Contains(string(b), `"two"`) {
		t.Errorf("a wrapped ring still claims the records it evicted:\n%s", b)
	}
	var drops int
	for _, r := range decode(t, b) {
		if r.Src != dropSrc {
			continue
		}
		drops++
		if r.Kind != dropKind {
			t.Errorf("the loss record reads kind %q", r.Kind)
		}
		if r.KV["n"] != "2" {
			t.Errorf("the loss record counts %q, want the 2 records evicted", r.KV["n"])
		}
		if r.CT != t0.UnixMilli() {
			t.Errorf("the loss record is stamped %d, want the clock of the emit that evicted", r.CT)
		}
	}
	if drops != 1 {
		t.Errorf("%d loss records in one batch, want exactly one:\n%s", drops, b)
	}
}

// A record the node kept is not a loss, however far the ring has moved past
// it, or every quiet client would report drops it never had.
func TestAWrapOverAcknowledgedRecordsIsNoLoss(t *testing.T) {
	c := New(4, "cid7abc")
	for _, m := range []string{"one", "two", "three", "four"} {
		c.Emit("nav", "push", m, nil, t0)
	}
	_, ack := c.PendingBatch()
	ack()
	for _, m := range []string{"five", "six", "seven"} {
		c.Emit("nav", "push", m, nil, t0)
	}
	b, _ := c.PendingBatch()
	for _, r := range decode(t, b) {
		if r.Src == dropSrc {
			t.Errorf("an acknowledged record was reported as a loss:\n%s", b)
		}
	}
}

// A message past the cap is cut, never dropped: that the thing happened is
// the part worth keeping, and the cut lands on a rune boundary so the line
// still reads.
func TestALongMessageIsCutOnARuneBoundary(t *testing.T) {
	msg := strings.Repeat("é", tracewire.MaxMsg) // two bytes each
	c := New(4, "cid7abc")
	c.Emit("text", "save", msg, nil, t0)
	b, _ := c.PendingBatch()
	recs := decode(t, b)
	if len(recs) != 1 {
		t.Fatalf("%d records, want the one that was emitted", len(recs))
	}
	got := recs[0].Msg
	if got == "" || len(got) > tracewire.MaxMsg {
		t.Fatalf("a %d-byte message was cut to %d bytes", len(msg), len(got))
	}
	if !strings.HasPrefix(msg, got) {
		t.Errorf("the cut message is not a prefix of what was emitted")
	}
	for _, r := range got {
		if r != 'é' {
			t.Fatalf("the cut split a rune: %q", r)
		}
	}
}

// The caller's map is the caller's. A record says what was true when it was
// emitted, so a map reused for the next record cannot rewrite the last one.
func TestEmitCopiesTheCallersMap(t *testing.T) {
	c := New(4, "cid7abc")
	kv := map[string]string{"req": "k3f9x2a"}
	c.Emit("nav", "push", "one", kv, t0)
	kv["req"] = "zzzzzzz"
	b, _ := c.PendingBatch()
	if got := decode(t, b)[0].KV["req"]; got != "k3f9x2a" {
		t.Errorf("the stored record reads req %q, want the value at emit", got)
	}
}

func lines(t *testing.T, b []byte) int {
	t.Helper()
	return len(decode(t, b))
}

func decode(t *testing.T, b []byte) []tracewire.Record {
	t.Helper()
	if len(b) > 0 && b[len(b)-1] != '\n' {
		t.Fatalf("the batch does not end in a newline: %q", b)
	}
	var out []tracewire.Record
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if line == "" {
			continue
		}
		var r tracewire.Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		out = append(out, r)
	}
	return out
}

// The cid is how a dump tells one page's records from another's, so the ring
// answers for it rather than every caller carrying a second copy.
func TestTheClientAnswersForItsCID(t *testing.T) {
	c := New(4, "cid7abc")
	if c.CID() != "cid7abc" {
		t.Errorf("CID is %q", c.CID())
	}
	c.Emit("nav", "push", "x", nil, t0)
	if r := decode(t, mustBatch(t, c))[0]; r.CID != c.CID() {
		t.Errorf("a record carries cid %q, want %q", r.CID, c.CID())
	}
}

// The ring says when it is owed something. Without it a record made by
// anything but the shim's own emit — the rpc interceptor writes here
// directly — would sit unposted until a later record armed the flush.
func TestEveryEmitSaysARecordIsOwed(t *testing.T) {
	c := New(4, "cid7abc")
	owed := 0
	c.OnEmit = func() { owed++ }
	c.Emit("rpc", "rpc", "GetGrid start", nil, t0)
	c.Emit("rpc", "rpc", "GetGrid ok", nil, t0)
	if owed != 2 {
		t.Errorf("%d arms for two records, want one each", owed)
	}
}
