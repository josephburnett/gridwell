package markdown

import (
	"strings"
	"testing"
)

func TestRenderedCSSScopes(t *testing.T) {
	css := RenderedCSS("#gw-rendered-view")
	if strings.Contains(css, "SCOPE") {
		t.Fatal("placeholder survived scoping")
	}
	if !strings.Contains(css, "#gw-rendered-view h1") {
		t.Fatal("rules not scoped under the selector")
	}
	// The preview raster and the overlay are the same stylesheet under
	// different scopes: byte-identical after normalizing the selector.
	prev := RenderedCSS(".gw-md-root")
	if strings.ReplaceAll(css, "#gw-rendered-view", "X") != strings.ReplaceAll(prev, ".gw-md-root", "X") {
		t.Fatal("overlay and preview stylesheets diverge")
	}
}

func TestPreviewSVGShape(t *testing.T) {
	svg := PreviewSVG("<p xmlns=\"http://www.w3.org/1999/xhtml\">hi</p>", 320, 4000, "#26262b")
	for _, want := range []string{
		`width="320"`, `height="4000"`,
		`<foreignObject`, `class="gw-md-root"`,
		`width:320px`, `background:#26262b`,
		`.gw-md-root h1`, `>hi</p>`,
	} {
		if !strings.Contains(svg, want) {
			t.Fatalf("svg missing %q:\n%s", want, svg)
		}
	}
}
