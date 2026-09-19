package textedit

import (
	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

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
		// The flip is between rendered and raw source, so raw stays
		// uneditable and "both" keeps the toggle either way.
		{"both, read-only", rpc.TextPresentationBoth, true, "status", true},

		{"plain, writable", rpc.TextPresentationPlain, false, "notes.md", false},
		{"plain, read-only", rpc.TextPresentationPlain, true, "notes.md", false},
	}
	for _, c := range cases {
		tile := &gridwellv1.Tile{Kind: rpc.KindText, TextPresentation: c.presentation, AltText: c.altText}
		if got := ToggleVisible(tile, c.readOnly); got != c.want {
			t.Errorf("%s: ToggleVisible = %v, want %v", c.name, got, c.want)
		}
	}
}

// The declaration picks the renderer; the name only picks the dialect.
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
		{"undeclared org name renders a heading", "", "notes.org", "<h2", ""},
		{"both declaration renders too", rpc.TextPresentationBoth, "notes.md", "<li", ""},
	}
	for _, c := range cases {
		tile := &gridwellv1.Tile{Kind: rpc.KindText, TextPresentation: c.presentation, AltText: c.altText}
		got := PresentationHTML(tile, body)
		if !strings.Contains(got, c.wantContains) {
			t.Errorf("%s: PresentationHTML = %q, want it to contain %q", c.name, got, c.wantContains)
		}
		if c.wantAbsent != "" && strings.Contains(got, c.wantAbsent) {
			t.Errorf("%s: PresentationHTML = %q, want it NOT to contain %q", c.name, got, c.wantAbsent)
		}
	}
}

func TestDecideCheckboxClick(t *testing.T) {
	cases := []struct {
		name                                    string
		textDocument, org, readOnly, bodyCached bool
		want                                    CheckboxClick
	}{
		{"an editable markdown document toggles", true, false, false, true, CheckboxToggle},
		{"a page tile reverts silently", false, false, false, true, CheckboxRevert},
		{"an org document reverts silently", true, true, false, true, CheckboxRevert},
		{"a read-only document says so", true, false, true, true, CheckboxReadOnly},
		{"a read-only org document still reverts silently", true, true, true, true, CheckboxRevert},
		{"a body not yet cached reverts silently", true, false, false, false, CheckboxRevert},
	}
	for _, c := range cases {
		if got := DecideCheckboxClick(c.textDocument, c.org, c.readOnly, c.bodyCached); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
