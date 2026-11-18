package outbox

import "testing"

// A claim is the version the gesture saw: a foreign writer's bump never moves
// it, so the write is refused rather than landing over a name nobody saw.
func TestClaimForeignBumpIsRefused(t *testing.T) {
	c := NewClaims()
	cl := c.Open("t", 5)
	// A foreign rename: the row is at 6, but nothing of this client landed.
	if got := c.Version(cl); got != 5 {
		t.Fatalf("claim after a foreign bump = %d, want the 5 the gesture saw", got)
	}
	// This client's own write claimed 6 (a version it never held) and landed:
	// it is not on this writer's basis, so the claim stays.
	c.Landed("t", 6, 7)
	if got := c.Version(cl); got != 5 {
		t.Fatalf("claim after another basis landed = %d, want 5", got)
	}
}

// This client's own write that claimed what the writer claims, and landed,
// moves the claim: a second rename queued behind the first does not
// conflict with it.
func TestClaimOwnLandedBumpIsAbsorbed(t *testing.T) {
	c := NewClaims()
	first := c.Open("t", 5)
	second := c.Open("t", 5)
	other := c.Open("u", 5)
	c.Landed("t", c.Version(first), 6)
	c.Close(first)
	if got := c.Version(second); got != 6 {
		t.Fatalf("second claim = %d, want 6 after the first landed", got)
	}
	if got := c.Version(other); got != 5 {
		t.Fatalf("another row's claim = %d, want 5", got)
	}
}

// A retry of a send the node never answered claims what the first send did.
func TestClaimRetryCarriesTheSameClaim(t *testing.T) {
	c := NewClaims()
	cl := c.Open("t", 5)
	first := c.Version(cl)
	// Unanswered: nothing landed. The retry reads the same claim.
	if retry := c.Version(cl); retry != first {
		t.Fatalf("retry claims %d, first send claimed %d", retry, first)
	}
}

// A closed claim no longer moves, so a writer that ended cannot be carried
// along by later writes.
func TestClaimClosedDoesNotMove(t *testing.T) {
	c := NewClaims()
	cl := c.Open("t", 5)
	c.Close(cl)
	c.Landed("t", 5, 6)
	if got := c.Version(cl); got != 5 {
		t.Fatalf("closed claim moved to %d", got)
	}
	if n := len(c.open); n != 0 {
		t.Fatalf("%d claims still open", n)
	}
}
