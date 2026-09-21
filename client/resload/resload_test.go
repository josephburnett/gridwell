package resload

import "testing"

// fakeRes records revocation, so a test can assert that nothing loaded is
// leaked and nothing live is released.
type fakeRes struct{ revoked bool }

func (r *fakeRes) Truthy() bool { return !r.revoked }
func (r *fakeRes) Revoke()      { r.revoked = true }

// TestTakeInstallsAndRevokesWhatItReplaces pins the revoke discipline: one
// live resource per entry, the previous one released as the new one lands.
func TestTakeInstallsAndRevokesWhatItReplaces(t *testing.T) {
	e := &Entry[int]{}
	first, second := &fakeRes{}, &fakeRes{}

	if !Take(e, e.Begin(), 1, first) {
		t.Fatal("the current load must install")
	}
	if !e.Ready() || e.Res != first || e.Ident != 1 {
		t.Fatalf("entry = %+v; want the first resource under ident 1", e)
	}
	if !Take(e, e.Begin(), 2, second) {
		t.Fatal("a later load must install")
	}
	if !first.revoked {
		t.Error("the replaced resource was not revoked")
	}
	if e.Res != second || e.Ident != 2 {
		t.Errorf("entry = %+v; want the second resource under ident 2", e)
	}
}

// TestSupersededResultsInstallNothing pins the generation guard on both
// verdicts: a load a newer Begin superseded neither installs its resource nor
// records its miss, and its resource is revoked rather than leaked.
func TestSupersededResultsInstallNothing(t *testing.T) {
	e := &Entry[int]{}
	stale, fresh := &fakeRes{}, &fakeRes{}

	staleGen := e.Begin()
	Take(e, e.Begin(), 2, fresh)

	if Take(e, staleGen, 1, stale) {
		t.Error("a superseded load installed")
	}
	if !stale.revoked {
		t.Error("a superseded result was not revoked; it leaks")
	}
	if e.Res != fresh || e.Ident != 2 {
		t.Errorf("entry = %+v; want the winner untouched", e)
	}
	if Miss(e, staleGen, 1) || e.Failed {
		t.Error("a superseded failure recorded a miss")
	}
}

// TestResultOfADroppedEntryIsRevoked pins the nil case, which is a key deleted
// while its load ran: the result has nowhere to go and must not leak.
func TestResultOfADroppedEntryIsRevoked(t *testing.T) {
	var dropped *Entry[int]
	res := &fakeRes{}
	if Take(dropped, 1, 1, res) {
		t.Error("a dropped key installed")
	}
	if !res.revoked {
		t.Error("the result of a dropped key was not revoked")
	}
	if Miss(dropped, 1, 1) {
		t.Error("a dropped key recorded a miss")
	}
}

// TestGenerationsAreNeverReused pins that a result cannot install into an entry
// remade under the same key while it ran. A per-entry counter would hand the
// new entry the generation the old load is carrying.
func TestGenerationsAreNeverReused(t *testing.T) {
	old := &Entry[int]{}
	inFlight := old.Begin()

	remade := &Entry[int]{} // the key was dropped and asked for again
	remade.Begin()

	res := &fakeRes{}
	if Take(remade, inFlight, 1, res) {
		t.Error("a load from the dropped entry installed into its replacement")
	}
	if !res.revoked {
		t.Error("the stale result was not revoked")
	}
}

// TestMissLatchesBesideALoadedResource pins the settled-failure latch and the
// one property that makes it separate from Ident: an entry holds one
// identity's resource and another's settled miss at once, so the caller stops
// re-asking for the identity that will never become a picture while the tile
// keeps the face it has.
func TestMissLatchesBesideALoadedResource(t *testing.T) {
	e := &Entry[int]{}
	held := &fakeRes{}
	Take(e, e.Begin(), 1, held)

	if !Miss(e, e.Begin(), 2) {
		t.Fatal("the current load's failure must settle")
	}
	if !e.Failed || e.FailIdent != 2 {
		t.Errorf("entry = %+v; want ident 2 settled as a miss", e)
	}
	if !e.Ready() || e.Res != held || e.Ident != 1 {
		t.Errorf("entry = %+v; want ident 1's resource still held", e)
	}

	// An installed resource is the fresher answer, so the latch lifts.
	Take(e, e.Begin(), 2, &fakeRes{})
	if e.Failed {
		t.Error("an install left the miss latched; the caller never re-asks")
	}
}

// TestSettleNeedsNoGeneration pins the caller-driven latch, which records an
// answer no load produced and leaves a loaded resource alone.
func TestSettleNeedsNoGeneration(t *testing.T) {
	e := &Entry[int]{}
	held := &fakeRes{}
	Take(e, e.Begin(), 1, held)

	e.Settle(9)
	if !e.Failed || e.FailIdent != 9 {
		t.Errorf("entry = %+v; want ident 9 settled", e)
	}
	if e.Res != held {
		t.Error("Settle released the resource")
	}
}

// TestAdoptDropsWhatItHeld pins the other replacement policy: an entry that
// answers for the load in flight releases its resource and its verdict at
// once, so nothing stale is drawn or reported for the new identity.
func TestAdoptDropsWhatItHeld(t *testing.T) {
	e := &Entry[int]{}
	held := &fakeRes{}
	Take(e, e.Begin(), 1, held)
	Miss(e, e.Begin(), 1)

	e.Adopt(2)
	if !held.revoked {
		t.Error("Adopt did not revoke the resource it dropped")
	}
	if e.Ready() || e.Failed || e.Ident != 2 {
		t.Errorf("entry = %+v; want ident 2, nothing held, nothing settled", e)
	}
}

// TestReleaseIsIdempotent pins that a second release of an entry, which a
// double Drop is, revokes nothing twice.
func TestReleaseIsIdempotent(t *testing.T) {
	e := &Entry[int]{}
	e.Release()
	Take(e, e.Begin(), 1, &fakeRes{})
	e.Release()
	if e.Ready() {
		t.Error("a released entry still reports ready")
	}
	e.Release()
}
