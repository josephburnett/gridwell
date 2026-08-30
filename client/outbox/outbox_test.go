package outbox

import (
	"errors"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/client/clientsync"
)

func k(op, id string) Key { return Key{Op: op, ID: id} }

func TestParkAckDrain(t *testing.T) {
	o := New()
	var fired []string
	o.Park(k("SetFraming", "1"), func() { fired = append(fired, "framing1") })
	o.Park(k("SetTextView", "2"), func() { fired = append(fired, "text2") })
	o.Park(k("SetURLState", "3"), func() { fired = append(fired, "url3") })
	if o.Len() != 3 {
		t.Fatalf("Len = %d, want 3", o.Len())
	}

	// A completed attempt (success OR verdict) clears its key.
	o.Ack(k("SetTextView", "2"))
	if o.Len() != 2 {
		t.Fatalf("Len after Ack = %d, want 2", o.Len())
	}
	// Ack of an unknown key is a no-op.
	o.Ack(k("SetTextView", "999"))

	for _, fn := range o.Drain() {
		fn()
	}
	if got := len(fired); got != 2 || fired[0] != "framing1" || fired[1] != "url3" {
		t.Errorf("drained %v, want [framing1 url3] in first-parked order", fired)
	}
	if o.Len() != 0 {
		t.Errorf("outbox not empty after drain: %d", o.Len())
	}
}

// TestParkReplacesLastWriterWins pins the LWW rule: a newer parked value for
// the same key replaces the older thunk (these writes are LWW by design — the
// drain must land the newest viewport, not replay history), keeping the
// original drain position.
func TestParkReplacesLastWriterWins(t *testing.T) {
	o := New()
	var fired []string
	o.Park(k("SetFraming", "1"), func() { fired = append(fired, "old") })
	o.Park(k("SetURLState", "2"), func() { fired = append(fired, "url") })
	o.Park(k("SetFraming", "1"), func() { fired = append(fired, "new") })
	if o.Len() != 2 {
		t.Fatalf("Len = %d, want 2 (replace, not append)", o.Len())
	}
	for _, fn := range o.Drain() {
		fn()
	}
	if len(fired) != 2 || fired[0] != "new" || fired[1] != "url" {
		t.Errorf("drained %v, want [new url]: newest value, original position", fired)
	}
}

// TestReparkDuringDrain pins convergence on a still-dead link: a drained
// thunk whose retry fails on transport parks itself again (through Record),
// and the outbox must hold it for the next kick rather than lose it.
func TestReparkDuringDrain(t *testing.T) {
	o := New()
	attempts := 0
	dead := connect.NewError(connect.CodeUnavailable, errors.New("refused"))
	var retry func()
	retry = func() {
		attempts++
		o.Record(clientsync.Of(dead), k("SetFraming", "1"), retry)
	}
	o.Park(k("SetFraming", "1"), retry)

	for _, fn := range o.Drain() {
		fn()
	}
	if attempts != 1 || o.Len() != 1 {
		t.Fatalf("after failed drain: attempts=%d len=%d, want 1 and 1", attempts, o.Len())
	}
	// The link heals: the next drain fires it once, and Record with an OK
	// outcome acks instead of re-parking.
	for _, fn := range o.Drain() {
		_ = fn
		o.Record(clientsync.OutcomeOK, k("SetFraming", "1"), retry)
	}
	if o.Len() != 0 {
		t.Errorf("a landed write left %d parked", o.Len())
	}
}

// TestRecordIsTheOneRule is the whole reconcile table: ONLY a transport
// failure parks. Every other outcome means the server spoke — the write
// landed, lost a version race, or was refused — and the caller's own reaction
// (refetch, surface, drop the local copy) resolves it from there. A parked
// retry after a verdict would replay a write the server already answered.
func TestRecordIsTheOneRule(t *testing.T) {
	cases := []struct {
		out       clientsync.Outcome
		wantParks bool
	}{
		{clientsync.OutcomeOK, false},
		{clientsync.OutcomeConflict, false},
		{clientsync.OutcomeRejected, false},
		{clientsync.OutcomeTransport, true},
	}
	for _, c := range cases {
		o := New()
		o.Record(c.out, k("SetFraming", "1"), func() {})
		if got := o.Len() == 1; got != c.wantParks {
			t.Errorf("outcome %v: parked=%v, want %v", c.out, got, c.wantParks)
		}
	}
}

// TestRecordAcksAStaleParkOnSuccess: a write that lands must clear an entry
// an EARLIER attempt parked, or the next drain replays old state over the
// value the server now holds. Acking on every completion (not just failures)
// is what makes the last-writer-wins rule hold across a reconnect.
func TestRecordAcksAStaleParkOnSuccess(t *testing.T) {
	o := New()
	o.Park(k("SetFraming", "1"), func() { t.Error("stale parked write was replayed") })
	o.Record(clientsync.OutcomeOK, k("SetFraming", "1"), func() {})
	for _, fn := range o.Drain() {
		fn()
	}
	if o.Len() != 0 {
		t.Errorf("Len = %d, want 0", o.Len())
	}
}

// TestRecordWithNoRetryStillAcks: a write with nothing to park (a create, a
// drag whose ghost snaps back — the failure is visible on screen and IS the
// reconcile) must not leave a stale entry behind when it completes.
func TestRecordWithNoRetryStillAcks(t *testing.T) {
	o := New()
	o.Park(k("CreateText", "1"), func() { t.Error("replayed") })
	o.Record(clientsync.OutcomeTransport, k("CreateText", "1"), nil)
	if o.Len() != 0 {
		t.Errorf("Len = %d, want 0 (no retry to park)", o.Len())
	}
}

// TestKeysReportsDrainOrder: the observability read sees exactly what a drain
// would run, in the order it would run it, and leaves the outbox alone.
func TestKeysReportsDrainOrder(t *testing.T) {
	o := New()
	o.Park(k(OpContent, "9"), func() {})
	o.Park(k("SetFraming", "1"), func() {})
	o.Ack(k(OpContent, "9"))
	o.Park(k(OpContent, "9"), func() {})
	got := o.Keys()
	want := []Key{k("SetFraming", "1"), k(OpContent, "9")}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Keys = %v, want %v", got, want)
	}
	if o.Len() != 2 {
		t.Errorf("Keys mutated the outbox: Len = %d", o.Len())
	}
}
