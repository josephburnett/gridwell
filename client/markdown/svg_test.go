package markdown

import (
	"math"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/client/theme"
)

func TestRenderedCSSScopes(t *testing.T) {
	dark := theme.Of(theme.Dark)
	css := RenderedCSS("#gw-rendered-view", dark)
	if strings.Contains(css, "@") {
		t.Fatalf("a placeholder survived substitution:\n%s", css)
	}
	if !strings.Contains(css, dark.DocLink) {
		t.Fatal("the palette's link color is not in the sheet")
	}
	if !strings.Contains(css, "#gw-rendered-view h1") {
		t.Fatal("rules not scoped under the selector")
	}
	// The preview raster and the overlay are one stylesheet under two scopes.
	prev := RenderedCSS(".gw-md-root", dark)
	if strings.ReplaceAll(css, "#gw-rendered-view", "X") != strings.ReplaceAll(prev, ".gw-md-root", "X") {
		t.Fatal("overlay and preview stylesheets diverge")
	}
}

func TestPreviewSVGShape(t *testing.T) {
	light := theme.Of(theme.Light)
	svg := PreviewSVG("<p xmlns=\"http://www.w3.org/1999/xhtml\">hi</p>", 320, 4000, light)
	for _, want := range []string{
		`width="320"`, `height="4000"`,
		`<foreignObject`, `class="gw-md-root"`,
		`width:320px`, `background:` + light.FileInnerBg,
		`.gw-md-root h1`, `>hi</p>`,
	} {
		if !strings.Contains(svg, want) {
			t.Fatalf("svg missing %q:\n%s", want, svg)
		}
	}
}

// The focused overlay lays the document out at the raster's width, so an
// unfocused pane, which draws the raster, wraps the same lines: the content
// box both leave is (layoutW - 2*RenderedPadX) at scale.
func TestRenderedInsetLaysOutAtTheRastersWidth(t *testing.T) {
	for _, c := range []struct{ boxW, layoutW, scale float64 }{
		{565, 512, 1}, {565, 565, 1}, {900, 640, 1.25}, {300, 410, 0.7},
	} {
		top, right, bottom, left := RenderedInset(c.boxW, c.layoutW, c.scale)
		got := c.boxW - left - right
		want := (c.layoutW - 2*RenderedPadX) * c.scale
		if math.Abs(got-want) > 1e-9 {
			t.Errorf("%+v: overlay content %v px, raster content %v px", c, got, want)
		}
		if top != RenderedPadY*c.scale || bottom != top || left != RenderedPadX*c.scale {
			t.Errorf("%+v: inset %v %v %v, want the raster's scaled", c, top, bottom, left)
		}
		if right < left {
			t.Errorf("%+v: right %v narrower than the document's own inset", c, right)
		}
	}
}

// A picture holds no form control: iOS WebKit paints an SVG image whose
// foreignObject holds an <input> as nothing at all, while the load succeeds.
// The task checkbox becomes the stand-in, its checked state carried, in every
// shape the overlay's renderer or XMLSerializer writes it.
func TestPreviewSVGHoldsNoFormControl(t *testing.T) {
	dark := theme.Of(theme.Dark)
	for _, c := range []struct {
		name, xhtml   string
		open, checked int
	}{
		{"renderer", RenderHTML([]byte("- [ ] a\n- [x] b\n- [ ] c\n"), false), 2, 1},
		{"serialized", `<ul><li><input type="checkbox" /> a</li><li><input checked="" type="checkbox"/> b</li></ul>`, 1, 1},
		{"attribute order", `<p><input type="checkbox" checked="checked" disabled=""/> x <INPUT TYPE='checkbox'> y</p>`, 1, 1},
		{"other input", `<p><input type="text" value="v"/>z</p>`, 0, 0},
	} {
		svg := PreviewSVG(c.xhtml, 320, 4000, dark)
		body := svg[strings.Index(svg, "</style>"):]
		if strings.Contains(strings.ToLower(body), "<input") {
			t.Errorf("%s: the picture holds a form control:\n%s", c.name, body)
		}
		if got := strings.Count(body, `<span class="gw-task"></span>`); got != c.open {
			t.Errorf("%s: %d open stand-ins, want %d:\n%s", c.name, got, c.open, body)
		}
		if got := strings.Count(body, `<span class="gw-task gw-task-done"></span>`); got != c.checked {
			t.Errorf("%s: %d checked stand-ins, want %d:\n%s", c.name, got, c.checked, body)
		}
	}
}

// The overlay's checkbox and the picture's stand-in are drawn by the same
// rules, so the two faces lay a task list out alike: every rule that styles
// one face styles the other in the same selector list.
func TestRenderedCSSDrawsTheCheckboxAndItsStandInAlike(t *testing.T) {
	css := RenderedCSS("S", theme.Of(theme.Dark))
	pairs := []struct{ input, standIn string }{
		{"S input[type=checkbox]", "S .gw-task"},
		{"S input[type=checkbox]:checked", "S .gw-task.gw-task-done"},
		{"S input[type=checkbox]:checked::after", "S .gw-task.gw-task-done::after"},
	}
	seen := make([]bool, len(pairs))
	for _, rule := range strings.Split(css, "}") {
		open := strings.Index(rule, "{")
		if open < 0 {
			continue
		}
		sels := map[string]bool{}
		for _, s := range strings.Split(rule[:open], ",") {
			sels[strings.TrimSpace(s)] = true
		}
		for i, p := range pairs {
			if sels[p.input] != sels[p.standIn] {
				t.Errorf("rule %q styles one face of the task box only", strings.TrimSpace(rule[:open]))
			}
			if sels[p.input] {
				seen[i] = true
				if !strings.Contains(rule, "appearance: none") && i == 0 {
					t.Errorf("the overlay's checkbox keeps its native look: %q", rule)
				}
			}
		}
		for s := range sels {
			if strings.Contains(s, "checkbox") || strings.Contains(s, "gw-task") {
				known := false
				for _, p := range pairs {
					known = known || s == p.input || s == p.standIn
				}
				if !known {
					t.Errorf("selector %q styles one face of the task box only", s)
				}
			}
		}
	}
	for i, p := range pairs {
		if !seen[i] {
			t.Errorf("no rule draws %q", p.input)
		}
	}
}
