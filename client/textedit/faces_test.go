package textedit

import (
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

// The whole toggle rule as a table: (presentation, read-only, name). A
// declared presentation is the authority and the name never enters into it;
// only an undeclared tile falls to the default, where a writable doc always
// toggles and a read-only one toggles when its name is renderable.
func TestToggleVisibleTable(t *testing.T) {
	cases := []struct {
		name         string
		presentation string
		readOnly     bool
		altText      string
		want         bool
	}{
		{"undeclared, writable, md", "", false, "notes.md", true},
		{"undeclared, writable, unrenderable name", "", false, "notes.txt", true},
		{"undeclared, writable, no name", "", false, "", true},
		{"undeclared, read-only, md", "", true, "notes.md", true},
		{"undeclared, read-only, org", "", true, "notes.org", true},
		{"undeclared, read-only, unrenderable name", "", true, "status", false},

		{"both, writable", rpc.TextPresentationBoth, false, "notes.md", true},
		// "both" keeps the toggle whether or not the tile is writable: the
		// flip is between rendered and raw SOURCE, and raw stays uneditable.
		{"both, read-only", rpc.TextPresentationBoth, true, "status", true},

		{"plain, writable", rpc.TextPresentationPlain, false, "notes.md", false},
		{"plain, read-only", rpc.TextPresentationPlain, true, "notes.md", false},
		{"rendered, writable", rpc.TextPresentationRendered, false, "notes.md", false},
		{"rendered, read-only", rpc.TextPresentationRendered, true, "notes.md", false},
	}
	for _, c := range cases {
		tile := rpc.Tile{Kind: rpc.KindText, TextPresentation: c.presentation, AltText: c.altText}
		if got := ToggleVisible(&tile, c.readOnly); got != c.want {
			t.Errorf("%s: ToggleVisible = %v, want %v", c.name, got, c.want)
		}
	}
}

// The renderer follows the declaration, and the name only chooses between the
// two document dialects. "plain" is verbatim: the marker text survives as
// characters instead of becoming a heading.
func TestPresentationHTMLTable(t *testing.T) {
	body := []byte("* one\n")
	cases := []struct {
		name         string
		presentation string
		altText      string
		wantContains string
		wantAbsent   string
	}{
		{"plain is verbatim", rpc.TextPresentationPlain, "notes.md", "* one", "<h2"},
		{"undeclared markdown name renders a list", "", "notes.md", "<li", "<h2"},
		// The same source is a headline in org, not a list item: the name is
		// what picks the dialect.
		{"undeclared org name renders a heading", "", "notes.org", "<h2", ""},
		{"rendered declaration renders too", rpc.TextPresentationRendered, "notes.md", "<li", ""},
		{"both declaration renders too", rpc.TextPresentationBoth, "notes.md", "<li", ""},
	}
	for _, c := range cases {
		tile := rpc.Tile{Kind: rpc.KindText, TextPresentation: c.presentation, AltText: c.altText}
		got := PresentationHTML(&tile, body)
		if !strings.Contains(got, c.wantContains) {
			t.Errorf("%s: PresentationHTML = %q, want it to contain %q", c.name, got, c.wantContains)
		}
		if c.wantAbsent != "" && strings.Contains(got, c.wantAbsent) {
			t.Errorf("%s: PresentationHTML = %q, want it NOT to contain %q", c.name, got, c.wantAbsent)
		}
	}
}
