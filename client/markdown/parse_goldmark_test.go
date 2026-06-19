package markdown

import (
	"testing"

	"github.com/yuin/goldmark/ast"
	east "github.com/yuin/goldmark/extension/ast"
)

// TestGoldmarkParsesGFM is the Phase-0 smoke test: it confirms goldmark is
// wired with the GFM extensions by parsing a table, a strikethrough, and a
// task list and finding the corresponding AST nodes. This is the dependency
// the rest of the pipeline (Lower/Layout) builds on.
func TestGoldmarkParsesGFM(t *testing.T) {
	src := []byte("# Title\n\n" +
		"| a | b |\n|---|---|\n| 1 | 2 |\n\n" +
		"~~struck~~ text\n\n" +
		"- [x] done\n- [ ] todo\n")
	root := parseAST(src)

	var tables, strikes, tasks, headings int
	err := ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n.(type) {
		case *east.Table:
			tables++
		case *east.Strikethrough:
			strikes++
		case *east.TaskCheckBox:
			tasks++
		case *ast.Heading:
			headings++
		}
		return ast.WalkContinue, nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if tables != 1 {
		t.Errorf("tables = %d, want 1 (GFM table extension not active?)", tables)
	}
	if strikes != 1 {
		t.Errorf("strikethroughs = %d, want 1", strikes)
	}
	if tasks != 2 {
		t.Errorf("task checkboxes = %d, want 2", tasks)
	}
	if headings != 1 {
		t.Errorf("headings = %d, want 1", headings)
	}
}
