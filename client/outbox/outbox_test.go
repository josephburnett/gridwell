package outbox

import (
	"errors"
	"testing"
	"time"

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

	// A completed attempt clears its key.
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

// TestParkReplacesLastWriterWins pins that a newer thunk for the same key
// replaces the older one and keeps its drain position.
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

// TestReparkDuringDrain pins convergence on a dead link: a drained thunk
// whose retry fails on transport parks itself again for the next kick.
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
	// Once the link heals, the next drain fires it once and Record acks.
	for _, fn := range o.Drain() {
		_ = fn
		o.Record(clientsync.OutcomeOK, k("SetFraming", "1"), retry)
	}
	if o.Len() != 0 {
		t.Errorf("a landed write left %d parked", o.Len())
	}
}

// TestRecordIsTheOneRule pins the reconcile table: only a transport failure
// parks. A parked retry after a verdict would replay a write the server has
// already answered.
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

// TestRecordAcksAStaleParkOnSuccess pins that a write which lands clears an
// entry an earlier attempt parked, so no drain replays old state over the
// value the server now holds.
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

// TestRecordWithNoRetryStillAcks pins that a write with nothing to park
// leaves no stale entry behind when it completes.
func TestRecordWithNoRetryStillAcks(t *testing.T) {
	o := New()
	o.Park(k("CreateText", "1"), func() { t.Error("replayed") })
	o.Record(clientsync.OutcomeTransport, k("CreateText", "1"), nil)
	if o.Len() != 0 {
		t.Errorf("Len = %d, want 0 (no retry to park)", o.Len())
	}
}

// TestKeysReportsDrainOrder pins that Keys sees what a drain would run, in
// order, and leaves the outbox alone.
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

// TestRecordContentIsTheDirtinessFork tables the content op's rule over
// dirty and parked. An ack for a key that is absent and clean must not
// resurrect it.
func TestRecordContentIsTheDirtinessFork(t *testing.T) {
	for _, tc := range []struct {
		name          string
		alreadyParked bool
		dirty         bool
		wantParked    bool
	}{
		{"a first keystroke parks", false, true, true},
		{"a later keystroke re-parks", true, true, true},
		{"a landed save acks", true, false, false},
		{"a clean tile nobody parked stays absent", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := New()
			if tc.alreadyParked {
				o.Park(k(OpContent, "t1"), func() {})
			}
			var fired int
			o.RecordContent("t1", tc.dirty, func() { fired++ })
			if got := o.Len() == 1; got != tc.wantParked {
				t.Fatalf("parked = %v, want %v (keys %v)", got, tc.wantParked, o.Keys())
			}
			if !tc.wantParked {
				return
			}
			if keys := o.Keys(); len(keys) != 1 || keys[0] != k(OpContent, "t1") {
				t.Fatalf("keys = %v, want one content key for t1", keys)
			}
			// The parked thunk is the one just handed over, so it
			// closes over the newest bytes.
			for _, fn := range o.Drain() {
				fn()
			}
			if fired != 1 {
				t.Errorf("the drained thunk fired %d times, want the newest one once", fired)
			}
		})
	}
}

// TestSyncContentParksTheDirtySetInOrder pins that the pre-drain sweep parks
// the dirty ids in the order given and touches nothing else. A parked key for
// a tile absent from the set keeps its place.
func TestSyncContentParksTheDirtySetInOrder(t *testing.T) {
	o := New()
	o.Park(k("SetFraming", "w1"), func() {})
	o.Park(k(OpContent, "gone"), func() {})

	var fired []string
	o.SyncContent([]string{"t1", "t2"}, func(id string) func() {
		return func() { fired = append(fired, id) }
	})

	want := []Key{k("SetFraming", "w1"), k(OpContent, "gone"), k(OpContent, "t1"), k(OpContent, "t2")}
	got := o.Keys()
	if len(got) != len(want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("keys = %v, want %v", got, want)
		}
	}
	// A second sweep gives the same keys in the same order.
	o.SyncContent([]string{"t1", "t2"}, func(id string) func() {
		return func() { fired = append(fired, id) }
	})
	if len(o.Keys()) != len(want) {
		t.Fatalf("a second sweep changed the outbox: %v", o.Keys())
	}
	for _, fn := range o.Drain() {
		fn()
	}
	if len(fired) != 2 || fired[0] != "t1" || fired[1] != "t2" {
		t.Errorf("drained %v, want [t1 t2] in the dirty set's order", fired)
	}
}

// TestSendParksBeforeTheAnswer pins that a write whose transport never
// answers is already in the outbox while it waits. A write recorded on its
// return would be recorded only if it returns.
func TestSendParksBeforeTheAnswer(t *testing.T) {
	o := New()
	blackhole := make(chan struct{})
	sent := make(chan struct{})
	go o.Send(k("SetFraming", "w1"), func() {}, func() clientsync.Outcome {
		close(sent)
		<-blackhole // a socket nobody answers and nobody resets
		return clientsync.OutcomeOK
	})
	<-sent

	// The call is out and no answer is coming, so the write stays parked,
	// drainable, and visible to Keys.
	deadline := time.Now().Add(2 * time.Second)
	for o.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if keys := o.Keys(); len(keys) != 1 || keys[0] != k("SetFraming", "w1") {
		t.Fatalf("a write waiting on a swallowed request is parked: keys = %v", keys)
	}
	close(blackhole)
}

// TestSendAcksOnTheVerdict pins that a verdict acks the key Send parked, so
// an ordinary write leaves nothing behind.
func TestSendAcksOnTheVerdict(t *testing.T) {
	dead := connect.NewError(connect.CodeUnavailable, errors.New("refused"))
	cases := []struct {
		name       string
		out        clientsync.Outcome
		wantParked bool
	}{
		{"landed", clientsync.Of(nil), false},
		{"conflict", clientsync.Of(connect.NewError(connect.CodeFailedPrecondition, errors.New("stale"))), false},
		{"rejected", clientsync.Of(connect.NewError(connect.CodeNotFound, errors.New("gone"))), false},
		{"transport", clientsync.Of(dead), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := New()
			got := o.Send(k("SetFraming", "w1"), func() {}, func() clientsync.Outcome {
				if o.Len() != 1 {
					t.Errorf("the write is parked while the call is out: len = %d", o.Len())
				}
				return c.out
			})
			if got != c.out {
				t.Errorf("Send returned %v, want the call's own outcome %v", got, c.out)
			}
			if parked := o.Len() == 1; parked != c.wantParked {
				t.Errorf("parked after %s = %v, want %v", c.name, parked, c.wantParked)
			}
		})
	}
}

// TestSendWithNoRetryParksNothing pins that a nil retry never becomes an
// entry the drain cannot fire.
func TestSendWithNoRetryParksNothing(t *testing.T) {
	o := New()
	dead := connect.NewError(connect.CodeUnavailable, errors.New("refused"))
	ran := false
	o.Send(k("PlaceTile", "t1"), nil, func() clientsync.Outcome {
		ran = true
		if o.Len() != 0 {
			t.Errorf("an unparked write parked anyway: %v", o.Keys())
		}
		return clientsync.Of(dead)
	})
	if !ran {
		t.Fatal("the call must run whether or not the write parks")
	}
	if o.Len() != 0 {
		t.Errorf("an unparked write left %v behind", o.Keys())
	}
}

// Whether a write is still owed is this package's fact, and a caller that
// says so out loud must read it here rather than re-deriving Record's rule.
func TestHasIsWhatIsStillOwed(t *testing.T) {
	o := New()
	k := Key{Op: "SetFraming", ID: "t7abcde"}
	if o.Has(k) {
		t.Error("a fresh outbox owes a write")
	}
	o.Send(k, func() {}, func() clientsync.Outcome { return clientsync.OutcomeTransport })
	if !o.Has(k) {
		t.Error("a write the server never answered is not owed")
	}
	o.Send(k, func() {}, func() clientsync.Outcome { return clientsync.OutcomeOK })
	if o.Has(k) {
		t.Error("a write the server kept is still owed")
	}
}
