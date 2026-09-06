package textedit

import "testing"

func TestSaveClaim(t *testing.T) {
	cases := []struct {
		name           string
		rowOwnsContent bool
		rowVersion     int64
		basis          int64
		haveBasis      bool
		want           int64
	}{
		// The basis is the answer whenever there is one: it is the version
		// of the bytes the edit derives from, and the row version is not.
		{"owner row, basis", true, 7, 5, true, 5},
		{"link row, basis", false, 7, 5, true, 5},
		// No entry to draw a basis from (a rejected write dropped it, or the
		// row was removed): the row snapshot stands in, but only when that
		// row owns the bytes.
		{"owner row, no basis", true, 7, 0, false, 7},
		{"link row, no basis", false, 7, 0, false, 0},
		{"no row, no basis", false, 0, 0, false, 0},
	}
	for _, c := range cases {
		if got := SaveClaim(c.rowOwnsContent, c.rowVersion, c.basis, c.haveBasis); got != c.want {
			t.Errorf("%s: SaveClaim = %d, want %d", c.name, got, c.want)
		}
	}
}

// TestSaveClaimFallbackIsWhatTheFlushSaw pins the drift this rule replaced.
// The ascent flush used to claim the row version re-read when the save
// reached the head of the queue, not the snapshot the flush decided from.
//
// The interleaving that makes the difference: a queued save is refused, its
// reconcile drops the content entry (so no basis is left) and refetches the
// grid, which lands a foreign writer's row at version 6. The ascent flush
// behind it still holds the bytes and the row it read at version 5. Claiming
// 6 vouches for bytes this client never saw and carries the stale buffer past
// the server's concurrency check, destroying the foreign edit silently.
// Claiming 5 is refused and reconciles visibly, which is the whole reason the
// claim exists.
func TestSaveClaimFallbackIsWhatTheFlushSaw(t *testing.T) {
	const sawAtFlush, foreignWriterRow = 5, 6
	if got := SaveClaim(true, sawAtFlush, 0, false); got != sawAtFlush {
		t.Errorf("fallback claimed %d, want the snapshot %d", got, sawAtFlush)
	}
	if got := SaveClaim(true, foreignWriterRow, 0, false); got == sawAtFlush {
		t.Fatal("SaveClaim must return what it is given: the caller owes it the snapshot")
	}
}
