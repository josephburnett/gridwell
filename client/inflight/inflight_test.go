package inflight

import (
	"context"
	"errors"
	"testing"
	"time"
)

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
	// The backstop: a request lost to a dead socket, with no reconnect to
	// cancel it, must still end. Without this the claim is held forever and
	// nothing ever asks again.
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

func TestCancelAllCancelsAndNamesEveryFetch(t *testing.T) {
	s := New(time.Minute)
	ctxA, _, _ := s.Begin("a")
	ctxB, _, _ := s.Begin("b")

	got := s.CancelAll()
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("CancelAll = %v, want the two keys sorted", got)
	}
	if !errors.Is(ctxA.Err(), context.Canceled) || !errors.Is(ctxB.Err(), context.Canceled) {
		t.Errorf("both fetches must be cancelled: a=%v b=%v", ctxA.Err(), ctxB.Err())
	}
	if s.Len() != 0 {
		t.Errorf("Len = %d, want no claim left standing", s.Len())
	}
	if _, _, ok := s.Begin("a"); !ok {
		t.Error("a cancelled key must be immediately claimable over the new link")
	}
}

func TestZombieReleaseKeepsTheFreshClaim(t *testing.T) {
	// The order that actually happens: the reconnect cancels the fetch, the
	// caller re-asks at once, and only then does the cancelled fetch return
	// and release. If that release freed the key, the fresh fetch would be
	// dogpiled by every frame that draws while it runs — and worse, it would
	// be the zombie's cancelled context that got released.
	s := New(time.Minute)
	_, zombieDone, _ := s.Begin("g1")
	s.CancelAll()

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
	if s.Len() != 0 {
		t.Errorf("Len = %d, want the fresh claim released by its own done", s.Len())
	}
}

func TestContextIsBoundedAndClaimFree(t *testing.T) {
	s := New(10 * time.Millisecond)
	ctx, cancel := s.Context()
	defer cancel()
	if s.Len() != 0 {
		t.Errorf("Len = %d, want an unclaimed fetch to hold no key", s.Len())
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("an unclaimed fetch must be bounded too")
	}
}
