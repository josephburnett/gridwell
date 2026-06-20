package shellconn

import "testing"

// TestSessionDeadOnClose pins the rule that only the server's explicit 1008
// PolicyViolation is a definitive "session gone" signal. The regression is the
// abnormal-closure case (1006): it must NOT be treated as dead-or-alive but as
// unknown (SessionDeadOnClose == false → caller re-probes), because the old
// code cached a dead session as alive on any non-1008 close.
func TestSessionDeadOnClose(t *testing.T) {
	cases := []struct {
		name string
		code int
		dead bool
	}{
		{"policy violation = session gone", 1008, true},
		{"normal closure is not definitive-dead", 1000, false},
		{"abnormal closure (1006) is not dead — re-probe", 1006, false},
		{"server error (1011) is not dead — re-probe", 1011, false},
		{"missing code is not dead — re-probe", -1, false},
		{"zero code is not dead — re-probe", 0, false},
		{"going away (1001) is not dead — re-probe", 1001, false},
		{"no status received (1005) is not dead — re-probe", 1005, false},
		{"app-defined code (4000) is not dead — re-probe", 4000, false},
		{"code just below 1008 is not dead", 1007, false},
		{"code just above 1008 is not dead", 1009, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SessionDeadOnClose(c.code); got != c.dead {
				t.Errorf("SessionDeadOnClose(%d) = %v, want %v", c.code, got, c.dead)
			}
		})
	}
}

func TestDecideShellRefreshVisible(t *testing.T) {
	cases := []struct {
		name                string
		isShell, hasPreview bool
		aliveKnown, alive   bool
		wantShow, wantProbe bool
	}{
		{"not a shell tile -> hidden, no probe", false, true, true, true, false, false},
		{"fresh tile (no preview) -> always show", true, false, false, false, true, false},
		{"fresh tile, even if cached dead -> show", true, false, true, false, true, false},
		{"has preview, cached alive -> show", true, true, true, true, true, false},
		{"has preview, cached dead -> hide", true, true, true, false, false, false},
		{"has preview, unknown -> hide + probe", true, true, false, false, false, true},
	}
	for _, c := range cases {
		got := DecideShellRefreshVisible(c.isShell, c.hasPreview, c.aliveKnown, c.alive)
		if got.Show != c.wantShow || got.Probe != c.wantProbe {
			t.Errorf("%s: got %+v, want Show=%v Probe=%v", c.name, got, c.wantShow, c.wantProbe)
		}
	}
}
