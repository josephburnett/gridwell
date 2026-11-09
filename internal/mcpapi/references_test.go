package mcpapi

import (
	"os"
	"strings"
	"testing"
)

// The instructions' references section is the one docs/mcp.md gives under
// "Gridwell references", word for word but for the page's code marks.
func TestDocsSayTheReferencesSection(t *testing.T) {
	doc, err := os.ReadFile("../../docs/mcp.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(doc), "\n## Gridwell references\n")
	if !ok {
		t.Fatal("docs/mcp.md has no Gridwell references section")
	}
	section, _, _ = strings.Cut(section, "\n## ")
	words := func(s string) string { return strings.Join(strings.Fields(strings.ReplaceAll(s, "`", "")), " ") }
	if got, want := words(section), words(references); got != want {
		t.Errorf("docs/mcp.md says\n%s\nthe instructions say\n%s", got, want)
	}
	if !strings.Contains(instructions, references) {
		t.Error("the instructions do not carry the references section")
	}
}
