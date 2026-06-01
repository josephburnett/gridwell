package pane

import "testing"

func testColors() BorderColors {
	return BorderColors{
		Focused:      "FOCUS",
		FocusedFaded: "FOCUS_FADED",
		Root:         "ROOT",
		Text:         "TEXT",
		TextFaded:    "TEXT_FADED",
		URL:          "URL",
		URLFaded:     "URL_FADED",
		URLLive:      "URL_LIVE",
	}
}

func TestBorderColorRoot(t *testing.T) {
	got := BorderColor(BorderInput{}, testColors())
	if got != "ROOT" {
		t.Errorf("root pane: got %q, want ROOT", got)
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
	// Live wins over focus state.
	if got := BorderColor(in, testColors()); got != "URL_LIVE" {
		t.Errorf("live url + not focused: got %q, want URL_LIVE (live overrides focus)", got)
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

func TestBorderColorURLLiveBeatsCacheMiss(t *testing.T) {
	// Edge case: URLLive=true but TileKnown=false means we fall through
	// to the descent blue branch (URLLive is only consulted when the
	// kind is known to be URL). This documents and pins that behavior.
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
