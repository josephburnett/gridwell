package pane

import "testing"

func testColors() BorderColors {
	return BorderColors{
		Focused:        "FOCUS",
		FocusedFaded:   "FOCUS_FADED",
		Text:           "TEXT",
		TextFaded:      "TEXT_FADED",
		URL:            "URL",
		URLFaded:       "URL_FADED",
		URLLive:        "URL_LIVE",
		URLLiveFaded:   "URL_LIVE_FADED",
		Shell:          "SHELL",
		ShellFaded:     "SHELL_FADED",
		Exit:           "EXIT",
		ExitFaded:      "EXIT_FADED",
		Ephemeral:      "EPHEMERAL",
		EphemeralFaded: "EPHEMERAL_FADED",
	}
}

func TestBorderColorPluginRootGridIsBlue(t *testing.T) {
	// A pane at a plugin's root grid (no descent, not the doorway) is a
	// grid like any other — blue, not brown.
	if got := BorderColor(BorderInput{}, testColors()); got != "FOCUS_FADED" {
		t.Errorf("plugin root grid + not focused: got %q, want FOCUS_FADED", got)
	}
	if got := BorderColor(BorderInput{Focused: true}, testColors()); got != "FOCUS" {
		t.Errorf("plugin root grid + focused: got %q, want FOCUS", got)
	}
}

func TestBorderColorDescentNoTile(t *testing.T) {
	// Descended into something (DescentDepth > 0) but no text focus.
	in := BorderInput{DescentDepth: 1}
	if got := BorderColor(in, testColors()); got != "FOCUS_FADED" {
		t.Errorf("descent + not focused: got %q, want FOCUS_FADED", got)
	}
	in.Focused = true
	if got := BorderColor(in, testColors()); got != "FOCUS" {
		t.Errorf("descent + focused: got %q, want FOCUS", got)
	}
}

func TestBorderColorTextTile(t *testing.T) {
	in := BorderInput{
		HasTextFocus: true,
		DescentDepth: 1,
		TileKnown:    true,
		TileKind:     "text",
	}
	if got := BorderColor(in, testColors()); got != "TEXT_FADED" {
		t.Errorf("text + not focused: got %q, want TEXT_FADED", got)
	}
	in.Focused = true
	if got := BorderColor(in, testColors()); got != "TEXT" {
		t.Errorf("text + focused: got %q, want TEXT", got)
	}
}

func TestBorderColorURLTile(t *testing.T) {
	in := BorderInput{
		HasTextFocus: true,
		DescentDepth: 1,
		TileKnown:    true,
		TileKind:     "url",
	}
	if got := BorderColor(in, testColors()); got != "URL_FADED" {
		t.Errorf("frozen url + not focused: got %q, want URL_FADED", got)
	}
	in.Focused = true
	if got := BorderColor(in, testColors()); got != "URL" {
		t.Errorf("frozen url + focused: got %q, want URL", got)
	}
	in.URLLive = true
	if got := BorderColor(in, testColors()); got != "URL_LIVE" {
		t.Errorf("live url + focused: got %q, want URL_LIVE", got)
	}
	in.Focused = false
	// Live fades like every other family when the pane loses focus.
	// Overriding focus would make a url pane read as permanently active.
	if got := BorderColor(in, testColors()); got != "URL_LIVE_FADED" {
		t.Errorf("live url + not focused: got %q, want URL_LIVE_FADED", got)
	}
}

func TestBorderColorUnknownTile(t *testing.T) {
	// Tile kind set but cache miss: TileKnown is false.
	in := BorderInput{
		HasTextFocus: true,
		DescentDepth: 1,
		TileKind:     "text", // ignored because TileKnown=false
		Focused:      true,
	}
	if got := BorderColor(in, testColors()); got != "FOCUS" {
		t.Errorf("unknown tile + focused: got %q, want FOCUS (descent blue fallback)", got)
	}
	in.Focused = false
	if got := BorderColor(in, testColors()); got != "FOCUS_FADED" {
		t.Errorf("unknown tile + not focused: got %q, want FOCUS_FADED", got)
	}
}

func TestBorderColorUnknownKindFallback(t *testing.T) {
	// TileKnown=true but the kind isn't text/url (e.g., a well lookup
	// that surfaced via TextFocus path).
	in := BorderInput{
		HasTextFocus: true,
		DescentDepth: 1,
		TileKnown:    true,
		TileKind:     "well",
		Focused:      true,
	}
	if got := BorderColor(in, testColors()); got != "FOCUS" {
		t.Errorf("known non-content kind + focused: got %q, want FOCUS", got)
	}
}

func TestBorderColorHostGridIsBlue(t *testing.T) {
	// Viewing a host-content grid is still viewing a grid.
	in := BorderInput{
		DescentDepth: 1,
		InHostGrid:   true,
	}
	if got := BorderColor(in, testColors()); got != "FOCUS_FADED" {
		t.Errorf("host grid + not focused: got %q, want FOCUS_FADED", got)
	}
	in.Focused = true
	if got := BorderColor(in, testColors()); got != "FOCUS" {
		t.Errorf("source grid + focused: got %q, want FOCUS", got)
	}
}

func TestBorderColorShellTile(t *testing.T) {
	// Descent into a shell tile is orange — bash runs outside Gridwell.
	in := BorderInput{
		HasTextFocus: true,
		DescentDepth: 1,
		TileKnown:    true,
		TileKind:     "shell",
	}
	if got := BorderColor(in, testColors()); got != "SHELL_FADED" {
		t.Errorf("shell + not focused: got %q, want SHELL_FADED", got)
	}
	in.Focused = true
	if got := BorderColor(in, testColors()); got != "SHELL" {
		t.Errorf("shell + focused: got %q, want SHELL", got)
	}
}

func TestBorderColorTextInHostGridIsExit(t *testing.T) {
	// A text tile inside a host-content grid is a read-only window onto host
	// state, so the editable color would lie. A url tile in one is still a url.
	in := BorderInput{
		HasTextFocus: true,
		DescentDepth: 1,
		TileKnown:    true,
		TileKind:     "text",
		InHostGrid:   true,
		Focused:      true,
	}
	if got := BorderColor(in, testColors()); got != "EXIT" {
		t.Errorf("text-in-host-grid + focused: got %q, want EXIT", got)
	}
	in.Focused = false
	if got := BorderColor(in, testColors()); got != "EXIT_FADED" {
		t.Errorf("text-in-source-grid + not focused: got %q, want EXIT_FADED", got)
	}
}

func TestBorderColorURLLiveBeatsCacheMiss(t *testing.T) {
	// URLLive is consulted only when the kind is known to be url.
	in := BorderInput{
		HasTextFocus: true,
		DescentDepth: 1,
		URLLive:      true,
		Focused:      true,
	}
	if got := BorderColor(in, testColors()); got != "FOCUS" {
		t.Errorf("URLLive without TileKnown: got %q, want FOCUS", got)
	}
}

// An ephemeral descent is gray whatever the kind: the border is the warning
// that ascent deletes the tile.
func TestBorderColorEphemeralTile(t *testing.T) {
	c := testColors()
	for _, kind := range []string{"url", "shell"} {
		in := BorderInput{HasTextFocus: true, TileKnown: true, TileKind: kind,
			Ephemeral: true, Focused: true, URLLive: true}
		if got := BorderColor(in, c); got != c.Ephemeral {
			t.Errorf("%s ephemeral focused = %q, want gray %q (live must not win)", kind, got, c.Ephemeral)
		}
		in.Focused = false
		if got := BorderColor(in, c); got != c.EphemeralFaded {
			t.Errorf("%s ephemeral unfocused = %q, want faded gray", kind, got)
		}
	}
	// Non-ephemeral keeps its kind color.
	in := BorderInput{HasTextFocus: true, TileKnown: true, TileKind: "shell", Focused: true}
	if got := BorderColor(in, c); got != c.Shell {
		t.Errorf("persistent shell = %q, want shell orange", got)
	}
}

// The one classifier the border and the bottom bar both derive from.
func TestFamilyOf(t *testing.T) {
	cases := []struct {
		name string
		in   BorderInput
		want Family
	}{
		{"grid", BorderInput{}, FamilyGrid},
		{"text", BorderInput{HasTextFocus: true, TileKnown: true, TileKind: "text"}, FamilyText},
		{"host text", BorderInput{HasTextFocus: true, TileKnown: true, TileKind: "text", InHostGrid: true}, FamilyExit},
		{"url frozen", BorderInput{HasTextFocus: true, TileKnown: true, TileKind: "url"}, FamilyURL},
		{"url live", BorderInput{HasTextFocus: true, TileKnown: true, TileKind: "url", URLLive: true}, FamilyURLLive},
		{"shell", BorderInput{HasTextFocus: true, TileKnown: true, TileKind: "shell"}, FamilyShell},
		{"ephemeral beats kind", BorderInput{HasTextFocus: true, TileKnown: true, TileKind: "shell", Ephemeral: true}, FamilyEphemeral},
		{"unknown tile falls back to grid", BorderInput{HasTextFocus: true}, FamilyGrid},
	}
	for _, c := range cases {
		if got := FamilyOf(c.in); got != c.want {
			t.Errorf("%s: FamilyOf = %v, want %v", c.name, got, c.want)
		}
	}
}
