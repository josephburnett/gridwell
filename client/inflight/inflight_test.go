package inflight

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// everyKey is the scope of a broken client-to-server link.
func everyKey(string) bool { return true }

func TestBeginDedupesAndDoneReleases(t *testing.T) {
	s := New(time.Minute)
	_, done, ok := s.Begin("g1")
	if !ok {
		t.Fatal("the first claim on a free key must be granted")
	}
	if _, _, ok := s.Begin("g1"); ok {
		t.Error("a second fetch for a key already in flight must be refused")
	}
	if _, _, ok := s.Begin("g2"); !ok {
		t.Error("a different key is a different claim")
	}
	done()
	if _, _, ok := s.Begin("g1"); !ok {
		t.Error("a released key must be claimable again")
	}
}

func TestDoneCancelsItsContext(t *testing.T) {
	s := New(time.Minute)
	ctx, done, _ := s.Begin("g1")
	done()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Errorf("a released claim's context must be cancelled, not left holding a timer: %v", ctx.Err())
	}
}

func TestDeadlineBoundsAFetchThatNeverAnswers(t *testing.T) {
	// A request lost to a dead socket, with no reconnect to cancel it,
	// still ends, so the claim is not held forever.
	s := New(10 * time.Millisecond)
	ctx, _, _ := s.Begin("g1")
	select {
	case <-ctx.Done():
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Errorf("err = %v, want DeadlineExceeded", ctx.Err())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a fetch that never answers must be bounded by the deadline")
	}
}

func TestCancelIfOverEveryKeyCancelsAndNamesEveryFetch(t *testing.T) {
	s := New(time.Minute)
	ctxA, _, _ := s.Begin("a")
	ctxB, _, _ := s.Begin("b")

	got := s.CancelIf(everyKey)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("CancelIf(everyKey) = %v, want the two keys sorted", got)
	}
	if !errors.Is(ctxA.Err(), context.Canceled) || !errors.Is(ctxB.Err(), context.Canceled) {
		t.Errorf("both fetches must be cancelled: a=%v b=%v", ctxA.Err(), ctxB.Err())
	}
	if len(s.Keys()) != 0 {
		t.Errorf("Len = %d, want no claim left standing", len(s.Keys()))
	}
	if _, _, ok := s.Begin("a"); !ok {
		t.Error("a cancelled key must be immediately claimable over the new link")
	}
}

// One source going dark kills only the fetches that rode through it. The
// others are still owed an answer over a link that never broke.
func TestCancelIfLeavesTheFetchesThatKeptTheirLink(t *testing.T) {
	s := New(time.Minute)
	dark, _, _ := s.Begin("n1abcde/laptop/far9xyz/1")
	alive, _, _ := s.Begin("fs9xyzw/1")

	got := s.CancelIf(func(k string) bool { return strings.HasPrefix(k, "n1abcde/laptop/") })
	if len(got) != 1 || got[0] != "n1abcde/laptop/far9xyz/1" {
		t.Fatalf("CancelIf = %v, want only the dark source's key", got)
	}
	if !errors.Is(dark.Err(), context.Canceled) {
		t.Errorf("the dark source's fetch must be cancelled: %v", dark.Err())
	}
	if alive.Err() != nil {
		t.Errorf("an unrelated source's fetch must still be alive: %v", alive.Err())
	}
	if keys := s.Keys(); len(keys) != 1 || keys[0] != "fs9xyzw/1" {
		t.Errorf("Keys = %v, want the surviving claim still held", keys)
	}
}

func TestZombieReleaseKeepsTheFreshClaim(t *testing.T) {
	// The reconnect cancels the fetch, the caller re-asks at once, and
	// only then does the cancelled fetch return and release. Freeing the
	// key there would dogpile the fresh fetch on every frame that draws.
	s := New(time.Minute)
	_, zombieDone, _ := s.Begin("g1")
	s.CancelIf(everyKey)

	fresh, freshDone, ok := s.Begin("g1")
	if !ok {
		t.Fatal("the re-ask must be granted")
	}
	zombieDone()

	if got := s.Keys(); len(got) != 1 || got[0] != "g1" {
		t.Errorf("Keys = %v, want the fresh claim still held", got)
	}
	if fresh.Err() != nil {
		t.Errorf("the fresh fetch must still be alive: %v", fresh.Err())
	}
	if _, _, ok := s.Begin("g1"); ok {
		t.Error("the fresh claim must still dedupe")
	}
	freshDone()
	if len(s.Keys()) != 0 {
		t.Errorf("Len = %d, want the fresh claim released by its own done", len(s.Keys()))
	}
}

func TestContextIsBoundedAndClaimFree(t *testing.T) {
	s := New(10 * time.Millisecond)
	ctx, cancel := s.Context()
	defer cancel()
	if len(s.Keys()) != 0 {
		t.Errorf("Len = %d, want an unclaimed fetch to hold no key", len(s.Keys()))
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("an unclaimed fetch must be bounded too")
	}
}

// TestBoundedCarriesTheDeadline pins that a caller with no Set of its own
// gets Deadline and nothing it chose for itself.
func TestBoundedCarriesTheDeadline(t *testing.T) {
	before := time.Now()
	ctx, cancel := Bounded()
	defer cancel()
	dl, ok := ctx.Deadline()
	if !ok {
		t.Fatal("a bounded context must carry a deadline")
	}
	if d := dl.Sub(before); d > Deadline+time.Second || d < Deadline-time.Second {
		t.Errorf("deadline is %v out, want Deadline (%v)", d, Deadline)
	}
	cancel()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Errorf("the caller's cancel must end it: %v", ctx.Err())
	}
}

// The sequence a dropped refetch loses: a request is in flight, the thing it
// asks about changes, and the ask that change makes is refused. The answer
// already on the wire was taken before the change, so without the owed flag
// the cache keeps a snapshot older than the change that asked for it.
func TestARefusedAskIsOwedToTheHolder(t *testing.T) {
	s := New(time.Minute)
	server, cache, changed := "v1", "", false
	var fetch func()
	fetch = func() {
		_, done, ok := s.Begin("g1")
		if !ok {
			return
		}
		read := server // the answer leaves the server now
		if !changed {
			// The change lands while this request is on the wire, and the
			// ask it makes is the one Begin refuses.
			changed, server = true, "v2"
			fetch()
		}
		cache = read
		if done() {
			fetch()
		}
	}
	fetch()
	if cache != "v2" {
		t.Errorf("cache = %q, want %q: the ask refused mid-flight was dropped", cache, "v2")
	}
}

func TestOwedIsPerClaimAndClearsWithIt(t *testing.T) {
	s := New(time.Minute)
	_, done, _ := s.Begin("g1")
	if _, _, ok := s.Begin("g1"); ok {
		t.Fatal("a second fetch for a key already in flight must be refused")
	}
	if !done() {
		t.Error("the holder of a refused ask must be told it is owed a re-ask")
	}
	_, done2, ok := s.Begin("g1")
	if !ok {
		t.Fatal("a released key must be claimable again")
	}
	if done2() {
		t.Error("a fresh claim starts owed nothing; the flag died with its claim")
	}
}
