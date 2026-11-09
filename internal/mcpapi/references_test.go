package mcpapi

import (
	"os"
	"strings"
	"testing"
)

// The instructions' sections are the ones docs/mcp.md gives under the same
// headings, word for word but for the page's code marks.
func TestDocsSayTheInstructionsSections(t *testing.T) {
	doc, err := os.ReadFile("../../docs/mcp.md")
	if err != nil {
		t.Fatal(err)
	}
	words := func(s string) string { return strings.Join(strings.Fields(strings.ReplaceAll(s, "`", "")), " ") }
	for heading, want := range map[string]string{"Gridwell references": references, "Templates": templates} {
		_, section, ok := strings.Cut(string(doc), "\n## "+heading+"\n")
		if !ok {
			t.Errorf("docs/mcp.md has no %s section", heading)
			continue
		}
		section, _, _ = strings.Cut(section, "\n## ")
		if got := words(section); got != words(want) {
			t.Errorf("docs/mcp.md says\n%s\nthe instructions say\n%s", got, want)
		}
		if !strings.Contains(instructions, want) {
			t.Errorf("the instructions do not carry the %s section", heading)
		}
	}
}
