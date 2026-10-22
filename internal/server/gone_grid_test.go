package server_test

import (
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/client/clientsync"
)

// TestGoneGridReadsDead holds what the parity cell door/see it dead compares
// and cannot itself say: a door onto a grid its owner says is gone, home's
// destroyed from the trash or a plugin's whose door left the listing, draws
// dead in the client and reads dead, never as a refusal or an empty grid.
func TestGoneGridReadsDead(t *testing.T) {
	for _, o := range []parityOwner{onHome, onPlugin} {
		t.Run(string(o), func(t *testing.T) {
			t.Parallel()
			obs := checkDead(newParityFix(t), o, kDoor)
			if !strings.HasPrefix(obs.Draw, "dead=true") || obs.Read != clientsync.OutcomeDead.String() {
				t.Errorf("a door onto %s's gone grid: %+v, want it drawn and read dead", o, obs)
			}
		})
	}
}
