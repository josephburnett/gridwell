package markdown

import (
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
