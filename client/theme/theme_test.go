package theme

import (
	"reflect"
	"strings"
	"testing"
)

// The two palettes are one shape with two sets of values, so a drawing site
// can read any role without asking which theme is on.
func TestBothPalettesFillEveryRole(t *testing.T) {
	for _, th := range All() {
		p := Of(th)
		v := reflect.ValueOf(p)
		for i := 0; i < v.NumField(); i++ {
			if v.Field(i).String() == "" {
				t.Errorf("%v palette leaves %s empty", th, v.Type().Field(i).Name)
			}
		}
	}
}

// Two themes sharing a shade is deliberate only for the accent; anywhere else
// it means a role was copied across rather than chosen.
func TestLightDiffersFromDarkExceptTheAccent(t *testing.T) {
	shared := map[string]bool{
		"FocusBorder": true, "TileResize": true, "SwapArrow": true,
		"NoEntryFill": true,
	}
	d, l := reflect.ValueOf(Of(Dark)), reflect.ValueOf(Of(Light))
	for i := 0; i < d.NumField(); i++ {
		name := d.Type().Field(i).Name
		same := d.Field(i).String() == l.Field(i).String()
		if same && !shared[name] {
			t.Errorf("%s is the same shade in both palettes", name)
		}
		if !same && shared[name] {
			t.Errorf("%s is meant to be the one shade both palettes hold", name)
		}
	}
}

func TestDefaultIsDark(t *testing.T) {
	if Default() != Dark {
		t.Fatalf("Default() = %v, want Dark", Default())
	}
	if got := Of(Default()); got != Of(Dark) {
		t.Fatal("Of(Default()) is not the dark palette")
	}
}

// The one spelling a stored value, a menu id and the e2e hook share.
func TestStringsAndLabels(t *testing.T) {
	str := map[Theme]string{Dark: "dark", Light: "light"}
	label := map[Theme]string{Dark: "Dark mode", Light: "Light mode"}
	for _, th := range All() {
		if th.String() != str[th] {
			t.Errorf("%d.String() = %q, want %q", int(th), th.String(), str[th])
		}
		if th.Label() != label[th] {
			t.Errorf("%v.Label() = %q, want %q", th, th.Label(), label[th])
		}
	}
}

// A value written by one release is read by the next.
func TestParseRoundTrip(t *testing.T) {
	for _, th := range All() {
		got, ok := Parse(th.String())
		if !ok || got != th {
			t.Errorf("Parse(%q) = %v, %v; want %v, true", th.String(), got, ok, th)
		}
	}
}

// An unreadable or unwritten preference is the default, and says so.
func TestParseUnknownFallsBackToDefault(t *testing.T) {
	for _, s := range []string{"", "Dark", "solarized", "null"} {
		got, ok := Parse(s)
		if ok || got != Default() {
			t.Errorf("Parse(%q) = %v, %v; want %v, false", s, got, ok, Default())
		}
	}
}

// Every field reaches the DOM, named the one way the stylesheet spells it.
func TestCSSVarsCoverThePalette(t *testing.T) {
	p := Of(Dark)
	vars := p.CSSVars()
	if len(vars) != reflect.TypeOf(p).NumField() {
		t.Fatalf("CSSVars() has %d entries for %d fields", len(vars), reflect.TypeOf(p).NumField())
	}
	seen := map[string]bool{}
	for _, v := range vars {
		if !strings.HasPrefix(v.Name, "--gw-") {
			t.Errorf("%q is not a --gw- custom property", v.Name)
		}
		if seen[v.Name] {
			t.Errorf("%q is named twice", v.Name)
		}
		seen[v.Name] = true
	}
	for _, want := range []string{"--gw-bg", "--gw-file-inner-bg", "--gw-url-live-line", "--gw-doc-code-bg"} {
		if !seen[want] {
			t.Errorf("CSSVars() has no %s", want)
		}
	}
}

func TestCSSName(t *testing.T) {
	cases := map[string]string{
		"Bg":               "--gw-bg",
		"FileInnerBg":      "--gw-file-inner-bg",
		"URLFill":          "--gw-url-fill",
		"URLLiveLineFaded": "--gw-url-live-line-faded",
		"DocFg":            "--gw-doc-fg",
	}
	for field, want := range cases {
		if got := cssName(field); got != want {
			t.Errorf("cssName(%q) = %q, want %q", field, got, want)
		}
	}
}
