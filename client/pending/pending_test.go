package pending

import "testing"

func k(op, id string) Key { return Key{Op: op, ID: id} }

func TestPutAckDrain(t *testing.T) {
	l := New()
	var fired []string
	l.Put(k("SetWellView", "1"), func() { fired = append(fired, "well1") })
	l.Put(k("SetTextView", "2"), func() { fired = append(fired, "text2") })
	l.Put(k("SetURLState", "3"), func() { fired = append(fired, "url3") })
	if l.Len() != 3 {
		t.Fatalf("Len = %d, want 3", l.Len())
	}

	// A completed attempt (success OR verdict) clears its key.
	l.Ack(k("SetTextView", "2"))
	if l.Len() != 2 {
		t.Fatalf("Len after Ack = %d, want 2", l.Len())
	}
	// Ack of an unknown key is a no-op.
	l.Ack(k("SetTextView", "999"))

	for _, fn := range l.Drain() {
		fn()
	}
	if got := len(fired); got != 2 || fired[0] != "well1" || fired[1] != "url3" {
		t.Errorf("drained %v, want [well1 url3] in first-parked order", fired)
	}
	if l.Len() != 0 {
		t.Errorf("ledger not empty after drain: %d", l.Len())
	}
}

// TestPutReplacesLastWriterWins pins the LWW rule: a newer parked value for
// the same key replaces the older thunk (framing is LWW by design — the
// drain must land the newest viewport, not replay history), keeping the
// original drain position.
func TestPutReplacesLastWriterWins(t *testing.T) {
	l := New()
	var fired []string
	l.Put(k("SetWellView", "1"), func() { fired = append(fired, "old") })
	l.Put(k("SetURLState", "2"), func() { fired = append(fired, "url") })
	l.Put(k("SetWellView", "1"), func() { fired = append(fired, "new") })
	if l.Len() != 2 {
		t.Fatalf("Len = %d, want 2 (replace, not append)", l.Len())
	}
	for _, fn := range l.Drain() {
		fn()
	}
	if len(fired) != 2 || fired[0] != "new" || fired[1] != "url" {
		t.Errorf("drained %v, want [new url]: newest value, original position", fired)
	}
}

// TestReparkDuringDrain pins convergence on a still-dead link: a drained
// thunk whose retry fails on transport parks itself again, and the ledger
// must hold it for the next kick rather than lose it.
func TestReparkDuringDrain(t *testing.T) {
	l := New()
	attempts := 0
	var retry func()
	retry = func() {
		attempts++
		l.Put(k("SetWellView", "1"), retry) // transport failed again: re-park
	}
	l.Put(k("SetWellView", "1"), retry)

	for _, fn := range l.Drain() {
		fn()
	}
	if attempts != 1 || l.Len() != 1 {
		t.Fatalf("after failed drain: attempts=%d len=%d, want 1 and 1", attempts, l.Len())
	}
	// The link heals: the next drain fires it once; the thunk acks by not
	// re-parking (dispatcher behavior on success).
	drained := l.Drain()
	if len(drained) != 1 {
		t.Fatalf("second drain returned %d thunks, want 1", len(drained))
	}
}
