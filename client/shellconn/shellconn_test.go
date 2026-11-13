package shellconn

import (
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

// TestDecideShellRefreshVisible tables the refresh button's visibility,
// including that a tile with no preview always shows and an unknown tmux
// hides and probes.
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

// Descending engages: a url opens, an alive or fresh shell opens, a dead
// shell stays frozen, a host without the capability stays silently frozen,
// and unknown aliveness probes. A url tile whose source serves its page is a
// url descent like any other. The user's standing freeze beats every one of
// those, for either family.
func TestDecideAutoLive(t *testing.T) {
	cases := []struct {
		name                                                 string
		descent                                              rpc.Descent
		liveURL, liveShell, hasPreview, known, alive, frozen bool
		want                                                 AutoLive
	}{
		{"url on Electron opens", rpc.DescentURL, true, true, true, false, false, false, AutoLiveURL},
		{"url in a browser stays frozen", rpc.DescentURL, false, false, true, false, false, false, AutoLiveNone},
		// The user's standing freeze beats the engagement default until
		// the reconnect gesture clears it.
		{"user-frozen url stays frozen", rpc.DescentURL, true, true, true, false, false, true, AutoLiveNone},
		{"user-frozen shell stays frozen", rpc.DescentShell, true, true, true, true, true, true, AutoLiveNone},
		// A capture that came back empty leaves a frozen tile looking never
		// opened, and creating a second tmux behind the freeze would be a
		// tmux nobody asked for.
		{"user-frozen shell with no preview creates nothing", rpc.DescentShell, true, true, false, false, false, true, AutoLiveNone},
		{"page tile on Electron opens", rpc.DescentURL, true, true, false, false, false, false, AutoLiveURL},
		{"page tile in a browser stays frozen", rpc.DescentURL, false, false, false, false, false, false, AutoLiveNone},
		{"fresh shell creates", rpc.DescentShell, true, true, false, false, false, false, AutoLiveShell},
		{"alive shell reconnects", rpc.DescentShell, true, true, true, true, true, false, AutoLiveShell},
		{"dead shell stays frozen", rpc.DescentShell, true, true, true, true, false, false, AutoLiveNone},
		{"unknown shell probes", rpc.DescentShell, true, true, true, false, false, false, AutoLiveProbeShell},
		{"shell in a browser stays frozen", rpc.DescentShell, false, false, true, true, true, false, AutoLiveNone},
		{"text does nothing", rpc.DescentNone, true, true, true, true, true, false, AutoLiveNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DecideAutoLive(c.descent, c.liveURL, c.liveShell, c.hasPreview, c.known, c.alive, c.frozen)
			if got != c.want {
				t.Errorf("DecideAutoLive = %v, want %v", got, c.want)
			}
		})
	}
}

// A click on a url in a live shell is Gridwell's alone whenever the
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

// What a stream's end means (Joe, 2026-11-13: "A live shell should rejoin of
// course. A frozen one should not."): the tmux gone is a known dead answer and
// stays down; any other end forgets, so the next go-live probes; and only a
// drop, an end the node did not say, rejoins.
func TestDecideEnd(t *testing.T) {
	cases := []struct {
		name           string
		tmuxGone, said bool
		want           EndVerdict
	}{
		{"the tmux gone stays down, known dead", true, true, EndVerdict{Dead: true}},
		{"a takeover or the tmux client's exit is the node's word", false, true, EndVerdict{}},
		{"a socket that broke rejoins", false, false, EndVerdict{Rejoin: true}},
	}
	for _, c := range cases {
		if got := DecideEnd(c.tmuxGone, c.said); got != c.want {
			t.Errorf("%s: = %+v, want %+v", c.name, got, c.want)
		}
	}
	// The forgotten answer routes a snapshotted tile back through the probe,
	// so a rejoin over a tmux that went meanwhile finds it dead.
	v := DecideEnd(false, false)
	if got := DecideAutoLive(rpc.DescentShell, true, true, true, v.Dead, false, false); got != AutoLiveProbeShell {
		t.Fatalf("after a drop a snapshotted shell should probe, got %v", got)
	}
	// The user's freeze beats the rejoin as it beats a descent.
	if got := DecideAutoLive(rpc.DescentShell, true, true, true, v.Dead, false, true); got != AutoLiveNone {
		t.Fatalf("a frozen shell must not rejoin, got %v", got)
	}
}
