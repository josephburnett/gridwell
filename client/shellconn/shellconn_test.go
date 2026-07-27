package shellconn

import "testing"

// TestSessionDeadOnClose pins the rule that only the server's explicit 1008
// PolicyViolation is a definitive "session gone" signal. The regression is the
// abnormal-closure case (1006): it must NOT be treated as dead-or-alive but as
// unknown (SessionDeadOnClose == false → caller re-probes), because the old
// code cached a dead session as alive on any non-1008 close.
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

func TestDecodeJPEGDataURL(t *testing.T) {
	// "data:image/jpeg;base64," + base64("hi") = "aGk="
	out, ok := DecodeJPEGDataURL("data:image/jpeg;base64,aGk=")
	if !ok || string(out) != "hi" {
		t.Errorf("decode = %q ok=%v, want hi true", out, ok)
	}
	bad := []struct {
		name string
		in   string
	}{
		{"wrong prefix", "data:image/png;base64,aGk="},
		{"no prefix", "aGk="},
		{"prefix only (empty body)", "data:image/jpeg;base64,"},
		{"invalid base64 body", "data:image/jpeg;base64,!!!!"},
		{"empty string", ""},
	}
	for _, c := range bad {
		if out, ok := DecodeJPEGDataURL(c.in); ok || out != nil {
			t.Errorf("%s: expected (nil,false), got (%q,%v)", c.name, out, ok)
		}
	}
}

// The auto-live descent decision (issue #202): descending engages — url
// opens, an alive or fresh shell opens, a dead shell stays frozen, a
// capability-gated host stays silently frozen, unknown aliveness probes.
func TestDecideAutoLive(t *testing.T) {
	cases := []struct {
		name                                                             string
		kindURL, kindShell, liveURL, liveShell, hasPreview, known, alive bool
		want                                                             AutoLive
	}{
		{"url on Electron opens", true, false, true, true, true, false, false, AutoLiveURL},
		{"url in a browser stays frozen", true, false, false, false, true, false, false, AutoLiveNone},
		{"fresh shell creates", false, true, true, true, false, false, false, AutoLiveShell},
		{"alive shell reconnects", false, true, true, true, true, true, true, AutoLiveShell},
		{"dead shell stays frozen", false, true, true, true, true, true, false, AutoLiveNone},
		{"unknown shell probes", false, true, true, true, true, false, false, AutoLiveProbeShell},
		{"shell in a browser stays frozen", false, true, false, false, true, true, true, AutoLiveNone},
		{"text does nothing", false, false, true, true, true, true, true, AutoLiveNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DecideAutoLive(c.kindURL, c.kindShell, c.liveURL, c.liveShell, c.hasPreview, c.known, c.alive)
			if got != c.want {
				t.Errorf("DecideAutoLive = %v, want %v", got, c.want)
			}
		})
	}
}
