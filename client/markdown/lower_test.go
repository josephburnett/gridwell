package markdown

import "testing"

// kinds returns the block kinds of a node's children, for compact assertions.
func kinds(n Node) []NodeKind {
	var ks []NodeKind
	for _, c := range n.Children {
		ks = append(ks, c.Kind)
	}
	return ks
}

func TestLowerBlockStructure(t *testing.T) {
	doc := Lower([]byte(
		"# Title\n\n" +
			"a paragraph\n\n" +
			"> a quote\n\n" +
			"- one\n- two\n\n" +
			"```go\nx := 1\n```\n\n" +
			"1. first\n2. second\n\n" +
			"---\n",
	))
	got := kinds(doc)
	want := []NodeKind{
		NodeHeading, NodeParagraph, NodeBlockQuote, NodeList,
		NodeCodeBlock, NodeList, NodeThematicBreak,
	}
	if len(got) != len(want) {
		t.Fatalf("block kinds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("block %d = %v, want %v", i, got[i], want[i])
		}
	}
	if h := doc.Children[0]; h.Level != 1 || spanText(h.Spans) != "Title" {
		t.Errorf("heading = level %d %q, want level 1 'Title'", h.Level, spanText(h.Spans))
	}
	if cb := doc.Children[4]; cb.Lang != "go" || spanText(cb.Spans) != "x := 1" {
		t.Errorf("code block = lang %q body %q, want 'go' 'x := 1'", cb.Lang, spanText(cb.Spans))
	}
	if ul := doc.Children[3]; ul.Ordered || len(ul.Children) != 2 {
		t.Errorf("unordered list: ordered=%v items=%d, want false/2", ul.Ordered, len(ul.Children))
	}
	if ol := doc.Children[5]; !ol.Ordered || len(ol.Children) != 2 {
		t.Errorf("ordered list: ordered=%v items=%d, want true/2", ol.Ordered, len(ol.Children))
	}
}

func TestLowerInlineStyles(t *testing.T) {
	doc := Lower([]byte("plain **bold** _italic_ `code` ~~struck~~ [text](/5)"))
	p := doc.Children[0]
	if p.Kind != NodeParagraph {
		t.Fatalf("first block = %v, want paragraph", p.Kind)
	}
	// Find a span by its text and assert its style.
	find := func(text string) Span {
		for _, sp := range p.Spans {
			if sp.Text == text {
				return sp
			}
		}
		t.Fatalf("no span with text %q in %+v", text, p.Spans)
		return Span{}
	}
	if s := find("bold"); s.Style&StyleBold == 0 {
		t.Errorf("'bold' span not bold: %+v", s)
	}
	if s := find("italic"); s.Style&StyleItalic == 0 {
		t.Errorf("'italic' span not italic: %+v", s)
	}
	if s := find("code"); s.Style&StyleCode == 0 {
		t.Errorf("'code' span not code: %+v", s)
	}
	if s := find("struck"); s.Style&StyleStrike == 0 {
		t.Errorf("'struck' span not strikethrough: %+v", s)
	}
	if s := find("text"); s.Style&StyleLink == 0 || s.Href != "/5" {
		t.Errorf("'text' span not a link to /5: %+v", s)
	}
}

func TestLowerTaskList(t *testing.T) {
	doc := Lower([]byte("- [x] done\n- [ ] todo\n- plain\n"))
	list := doc.Children[0]
	if list.Kind != NodeList || len(list.Children) != 3 {
		t.Fatalf("list = %v with %d items", list.Kind, len(list.Children))
	}
	if c := list.Children[0].Checked; c == nil || !*c {
		t.Errorf("item 0 checked = %v, want true", c)
	}
	if c := list.Children[1].Checked; c == nil || *c {
		t.Errorf("item 1 checked = %v, want false", c)
	}
	if c := list.Children[2].Checked; c != nil {
		t.Errorf("item 2 checked = %v, want nil (not a task)", c)
	}
}

// spanText concatenates span text (test helper).
func spanText(spans []Span) string {
	out := ""
	for _, sp := range spans {
		out += sp.Text
	}
	return out
}

// TestLowerIntrawordUnderscoreFree confirms a Phase-1 acceptance criterion: the
// snake_case half-italic bug we hand-fixed in the old engine is gone for free
// under goldmark (CommonMark intraword-underscore rule), and the code-block
// body is captured whole (so a code-block-first doc's alt won't break links).
func TestLowerIntrawordUnderscoreFree(t *testing.T) {
	doc := Lower([]byte("call foo_bar_baz()"))
	for _, sp := range doc.Children[0].Spans {
		if sp.Style&StyleItalic != 0 {
			t.Errorf("intraword underscore emphasized: %+v", doc.Children[0].Spans)
		}
	}
	// Boundary emphasis still works.
	var italic bool
	for _, sp := range Lower([]byte("a _b_ c")).Children[0].Spans {
		if sp.Text == "b" && sp.Style&StyleItalic != 0 {
			italic = true
		}
	}
	if !italic {
		t.Error("boundary underscore emphasis lost")
	}
}
