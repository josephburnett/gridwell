package shellconn

import "testing"

// TestSessionDeadOnClose pins the rule that only the server's explicit 1008
// PolicyViolation is a definitive "session gone" signal. An abnormal closure
// (1006) is unknown, not dead-or-alive: SessionDeadOnClose is false and the
// caller re-probes.
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

// The auto-live descent decision: descending engages — url opens, an alive
// or fresh shell opens, a dead shell stays frozen, a capability-gated host
// stays silently frozen, unknown aliveness probes. A serves_page tile gets
// exactly the url verdict: a page tile and a url tile engage identically.
func TestDecideAutoLive(t *testing.T) {
	cases := []struct {
		name                                                                           string
		webContent, kindShell, liveURL, liveShell, hasPreview, known, alive, urlFrozen bool
		want                                                                           AutoLive
	}{
		{"url on Electron opens", true, false, true, true, true, false, false, false, AutoLiveURL},
		{"url in a browser stays frozen", true, false, false, false, true, false, false, false, AutoLiveNone},
		// The user's standing freeze beats the engagement default:
		// re-descending a deliberately frozen url stays frozen until the
		// reconnect gesture clears the intent.
		{"user-frozen url stays frozen", true, false, true, true, true, false, false, true, AutoLiveNone},
		{"page tile on Electron opens", true, false, true, true, false, false, false, false, AutoLiveURL},
		{"page tile in a browser stays frozen", true, false, false, false, false, false, false, false, AutoLiveNone},
		{"fresh shell creates", false, true, true, true, false, false, false, false, AutoLiveShell},
		{"alive shell reconnects", false, true, true, true, true, true, true, false, AutoLiveShell},
		{"dead shell stays frozen", false, true, true, true, true, true, false, false, AutoLiveNone},
		{"unknown shell probes", false, true, true, true, true, false, false, false, AutoLiveProbeShell},
		{"shell in a browser stays frozen", false, true, false, false, true, true, true, false, AutoLiveNone},
		{"text does nothing", false, false, true, true, true, true, true, false, AutoLiveNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DecideAutoLive(c.webContent, c.kindShell, c.liveURL, c.liveShell, c.hasPreview, c.known, c.alive, c.urlFrozen)
			if got != c.want {
				t.Errorf("DecideAutoLive = %v, want %v", got, c.want)
			}
		})
	}
}

// A click on a url in a live shell belongs to Gridwell alone whenever the
// application would otherwise be told about it too.
func TestDecideLinkPress(t *testing.T) {
	for _, c := range []struct {
		name        string
		hoveredURL  string
		tracking    string
		modifier    bool
		wantSwallow bool
	}{
		{"link, tracking application", "https://x/", "vt200", false, true},
		{"link, x10 tracking", "https://x/", "x10", false, true},
		{"link, any tracking", "https://x/", "any", false, true},
		{"link, no tracking: xterm's own selection start", "https://x/", "none", false, false},
		{"link, terminal did not answer", "https://x/", "", false, false},
		{"link, modifier: the escape hatch stays", "https://x/", "any", true, false},
		{"no link, tracking application", "", "any", false, false},
		{"no link, no tracking", "", "none", false, false},
	} {
		if got := DecideLinkPress(c.hoveredURL, c.tracking, c.modifier); got != c.wantSwallow {
			t.Errorf("%s: DecideLinkPress(%q, %q, %v) = %v, want %v",
				c.name, c.hoveredURL, c.tracking, c.modifier, got, c.wantSwallow)
		}
	}
}
