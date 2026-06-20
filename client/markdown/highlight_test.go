package markdown

import "testing"

// hlColorOf returns the color a highlighter assigned to the first run whose
// text equals want, or -1.
func hlColorOf(toks []hlToken, want string) ColorRole {
	for _, tk := range toks {
		if tk.Text == want {
			return tk.Color
		}
	}
	return -1
}

func TestHighlightGoKeywords(t *testing.T) {
	toks := highlight("func main() { return }", "go")
	if c := hlColorOf(toks, "func"); c != ColorSynKeyword {
		t.Errorf("'func' color = %v, want ColorSynKeyword", c)
	}
	if c := hlColorOf(toks, "return"); c != ColorSynKeyword {
		t.Errorf("'return' color = %v, want ColorSynKeyword", c)
	}
	if c := hlColorOf(toks, "main"); c != -1 {
		t.Errorf("'main' should be a plain run (merged), got color %v", c)
	}
}

func TestHighlightStringsCommentsNumbers(t *testing.T) {
	toks := highlight(`x := "hi" + 42 // tail`, "go")
	if c := hlColorOf(toks, `"hi"`); c != ColorSynString {
		t.Errorf("string color = %v, want ColorSynString", c)
	}
	if c := hlColorOf(toks, "42"); c != ColorSynNumber {
		t.Errorf("number color = %v, want ColorSynNumber", c)
	}
	if c := hlColorOf(toks, "// tail"); c != ColorSynComment {
		t.Errorf("comment color = %v, want ColorSynComment", c)
	}
}

func TestHighlightBlockCommentMultiline(t *testing.T) {
	toks := highlight("a /* one\ntwo */ b", "c")
	if c := hlColorOf(toks, "/* one\ntwo */"); c != ColorSynComment {
		t.Errorf("multi-line block comment not one comment run: %+v", toks)
	}
}

func TestHighlightPythonHashAndKeyword(t *testing.T) {
	toks := highlight("def f(): # c", "python")
	if c := hlColorOf(toks, "def"); c != ColorSynKeyword {
		t.Errorf("'def' color = %v, want keyword", c)
	}
	if c := hlColorOf(toks, "# c"); c != ColorSynComment {
		t.Errorf("'# c' color = %v, want comment", c)
	}
}

func TestHighlightUnknownLangIsPlain(t *testing.T) {
	toks := highlight("func main", "no-such-lang")
	if len(toks) != 1 || toks[0].Color != ColorCode || toks[0].Text != "func main" {
		t.Errorf("unknown language should be one plain run, got %+v", toks)
	}
}
