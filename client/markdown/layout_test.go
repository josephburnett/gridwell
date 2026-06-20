package markdown

import (
	"strings"
	"testing"
)

// monoMeasure is a deterministic measure: every rune is half the font size
// wide, regardless of style. Makes layout geometry exactly assertable.
func monoMeasure(text string, fontPx float64, _ SpanStyle, _ bool) float64 {
	return float64(len([]rune(text))) * fontPx * 0.5
}

// classifyStub mirrors the wasm classifier without importing client/embed: a
// "/<int>[/<int>...]" href is a tile embed; any other StyleEmbed span is a real
// image; everything else flows as text.
func classifyStub(sp Span) AtomKind {
	if looksLikeTileHref(sp.Href) {
		return AtomEmbed
	}
	if sp.Style&StyleEmbed != 0 {
		return AtomImage
	}
	return AtomNone
}

func looksLikeTileHref(href string) bool {
	if !strings.HasPrefix(href, "/") {
		return false
	}
	any := false
	for part := range strings.SplitSeq(href[1:], "/") {
		if part == "" {
			continue
		}
		if strings.TrimFunc(part, func(r rune) bool { return r >= '0' && r <= '9' }) != "" {
			return false
		}
		any = true
	}
	return any
}

func layoutOf(t *testing.T, src string, width float64) LayoutResult {
	t.Helper()
	return Layout(Lower([]byte(src)), monoMeasure, classifyStub, width, DefaultLayoutStyle())
}

func opsOfKind(r LayoutResult, k DrawOpKind) []DrawOp {
	var out []DrawOp
	for _, op := range r.Ops {
		if op.Kind == k {
			out = append(out, op)
		}
	}
	return out
}

func TestLayoutHeadingFontSize(t *testing.T) {
	st := DefaultLayoutStyle()
	r := layoutOf(t, "# Hi", 400)
	texts := opsOfKind(r, OpText)
	if len(texts) == 0 {
		t.Fatal("no text ops")
	}
	if texts[0].FontPx != st.HeadingPx[1] {
		t.Errorf("heading font = %v, want %v", texts[0].FontPx, st.HeadingPx[1])
	}
	if texts[0].Color != ColorHeading {
		t.Errorf("heading color = %v, want ColorHeading", texts[0].Color)
	}
}

func TestLayoutParagraphWraps(t *testing.T) {
	// Each 2-char word is 16px (16*0.5*2); a space is 8px. Width 45 (content
	// 45-2*pad) forces two words per line.
	r := layoutOf(t, "aa bb cc dd", 45+2*DefaultLayoutStyle().PadX)
	texts := opsOfKind(r, OpText)
	lineYs := map[float64]bool{}
	for _, op := range texts {
		lineYs[op.Y] = true
	}
	if len(lineYs) != 2 {
		t.Errorf("expected 2 wrapped lines, got %d distinct line-Ys (%v)", len(lineYs), texts)
	}
	if r.Height <= 0 {
		t.Errorf("height = %v, want > 0", r.Height)
	}
}

func TestLayoutCodeBlock(t *testing.T) {
	r := layoutOf(t, "```\nalpha\nbeta\n```", 400)
	if rects := opsOfKind(r, OpRect); len(rects) != 1 || rects[0].Color != ColorCodeBg {
		t.Errorf("want exactly one code-bg rect, got %+v", rects)
	}
	texts := opsOfKind(r, OpText)
	if len(texts) != 2 {
		t.Fatalf("want 2 code lines, got %d", len(texts))
	}
	for _, op := range texts {
		if !op.Mono || op.Color != ColorCode {
			t.Errorf("code line not mono/code-colored: %+v", op)
		}
	}
}

func TestLayoutBlockquoteIndents(t *testing.T) {
	st := DefaultLayoutStyle()
	r := layoutOf(t, "> quoted", 400)
	rects := opsOfKind(r, OpRect)
	if len(rects) != 1 || rects[0].Color != ColorQuoteBar {
		t.Fatalf("want one quote-bar rect, got %+v", rects)
	}
	texts := opsOfKind(r, OpText)
	if len(texts) == 0 || texts[0].X <= st.PadX {
		t.Errorf("quote text not indented: x=%v, pad=%v", texts[0].X, st.PadX)
	}
}

func TestLayoutListMarkersAndIndent(t *testing.T) {
	st := DefaultLayoutStyle()
	r := layoutOf(t, "- one\n- two", 400)
	texts := opsOfKind(r, OpText)
	var markers, content []DrawOp
	for _, op := range texts {
		if op.Text == "•" {
			markers = append(markers, op)
		} else {
			content = append(content, op)
		}
	}
	if len(markers) != 2 {
		t.Errorf("want 2 bullet markers, got %d (%v)", len(markers), texts)
	}
	for _, op := range content {
		if op.X < st.PadX+st.ListIndent {
			t.Errorf("list content not indented: x=%v, want >= %v", op.X, st.PadX+st.ListIndent)
		}
	}
}

func TestLayoutOrderedListNumbers(t *testing.T) {
	r := layoutOf(t, "1. first\n2. second\n3. third", 400)
	var markers []string
	for _, op := range opsOfKind(r, OpText) {
		if op.Text == "1." || op.Text == "2." || op.Text == "3." {
			markers = append(markers, op.Text)
		}
	}
	if len(markers) != 3 || markers[0] != "1." || markers[2] != "3." {
		t.Errorf("ordered markers = %v, want [1. 2. 3.]", markers)
	}
}

func TestLayoutTileEmbedIsAtomic(t *testing.T) {
	// A tile-link href becomes a native embed; surrounding text still flows.
	r := layoutOf(t, "see [label](/5) here", 1000)
	embeds := opsOfKind(r, OpEmbed)
	if len(embeds) != 1 {
		t.Fatalf("want 1 embed op, got %d", len(embeds))
	}
	if opsOfKind(r, OpImage) != nil {
		t.Error("tile link should be an embed, not an image")
	}
	if len(opsOfKind(r, OpText)) == 0 {
		t.Error("expected surrounding text ops alongside the embed")
	}
}

func TestLayoutRealImageIsOpImage(t *testing.T) {
	// A bare image with a non-tile src is a real image, not a tile embed.
	r := layoutOf(t, "before ![alt](https://x/pic.png) after", 1000)
	imgs := opsOfKind(r, OpImage)
	if len(imgs) != 1 {
		t.Fatalf("want 1 image op, got %d (embeds=%d)", len(imgs), len(opsOfKind(r, OpEmbed)))
	}
	if imgs[0].Src != "https://x/pic.png" || imgs[0].Alt != "alt" {
		t.Errorf("image op src/alt = %q/%q", imgs[0].Src, imgs[0].Alt)
	}
	if opsOfKind(r, OpEmbed) != nil {
		t.Error("real image should not be an embed")
	}
}

func TestLayoutThematicBreak(t *testing.T) {
	r := layoutOf(t, "a\n\n---\n\nb", 400)
	if rules := opsOfKind(r, OpRule); len(rules) != 1 {
		t.Errorf("want 1 rule op, got %d", len(rules))
	}
}

func findText(r LayoutResult, s string) (DrawOp, bool) {
	for _, op := range r.Ops {
		if op.Kind == OpText && op.Text == s {
			return op, true
		}
	}
	return DrawOp{}, false
}

func TestLayoutNestedListIndents(t *testing.T) {
	r := layoutOf(t, "- a\n    - b\n- c", 400)
	a, okA := findText(r, "a")
	b, okB := findText(r, "b")
	c, okC := findText(r, "c")
	if !okA || !okB || !okC {
		t.Fatalf("missing list content ops: a=%v b=%v c=%v", okA, okB, okC)
	}
	if b.X <= a.X {
		t.Errorf("nested item b (x=%v) not indented past a (x=%v)", b.X, a.X)
	}
	if b.X <= c.X {
		t.Errorf("nested item b (x=%v) not indented past sibling c (x=%v)", b.X, c.X)
	}
}

func TestLayoutTaskCheckboxMarkers(t *testing.T) {
	r := layoutOf(t, "- [x] done\n- [ ] todo", 400)
	if _, ok := findText(r, "☑"); !ok {
		t.Error("missing checked task marker ☑")
	}
	if _, ok := findText(r, "☐"); !ok {
		t.Error("missing unchecked task marker ☐")
	}
}

func TestLayoutTightVsLoose(t *testing.T) {
	tight := layoutOf(t, "- a\n- b\n- c", 400)
	loose := layoutOf(t, "- a\n\n- b\n\n- c", 400)
	if loose.Height <= tight.Height {
		t.Errorf("loose list height %v should exceed tight %v", loose.Height, tight.Height)
	}
}

func TestLayoutStrikethrough(t *testing.T) {
	r := layoutOf(t, "~~struck~~ text", 400)
	op, ok := findText(r, "struck")
	if !ok {
		t.Fatal("no 'struck' text op")
	}
	if op.Style&StyleStrike == 0 {
		t.Errorf("struck text missing StyleStrike: %+v", op)
	}
}

func TestLayoutHeadingLevels(t *testing.T) {
	h3, _ := findText(layoutOf(t, "### h3", 400), "h3")
	h4, _ := findText(layoutOf(t, "#### h4", 400), "h4")
	h6, _ := findText(layoutOf(t, "###### h6", 400), "h6")
	if !(h3.FontPx > h4.FontPx && h4.FontPx >= h6.FontPx) {
		t.Errorf("heading sizes not monotonic: h3=%v h4=%v h6=%v", h3.FontPx, h4.FontPx, h6.FontPx)
	}
}

func TestLayoutHardBreak(t *testing.T) {
	// Two trailing spaces = a hard break: the two words land on different lines.
	r := layoutOf(t, "alpha  \nbravo", 1000)
	a, okA := findText(r, "alpha")
	b, okB := findText(r, "bravo")
	if !okA || !okB {
		t.Fatalf("missing words: alpha=%v bravo=%v", okA, okB)
	}
	if a.Y == b.Y {
		t.Errorf("hard break did not split lines: both at y=%v", a.Y)
	}
	// A soft break (no trailing spaces) keeps them on one line.
	r2 := layoutOf(t, "alpha\nbravo", 1000)
	a2, _ := findText(r2, "alpha")
	b2, _ := findText(r2, "bravo")
	if a2.Y != b2.Y {
		t.Errorf("soft break should keep one line: alpha y=%v bravo y=%v", a2.Y, b2.Y)
	}
}

func TestLayoutNestedBlockquote(t *testing.T) {
	r := layoutOf(t, "> a\n>\n> > b", 400)
	a, okA := findText(r, "a")
	b, okB := findText(r, "b")
	if !okA || !okB {
		t.Fatalf("missing quote text: a=%v b=%v", okA, okB)
	}
	if b.X <= a.X {
		t.Errorf("nested quote b (x=%v) not indented past a (x=%v)", b.X, a.X)
	}
	if bars := opsOfKind(r, OpRect); len(bars) < 2 {
		t.Errorf("want >=2 quote bars for nested quote, got %d", len(bars))
	}
}

func TestLayoutTableBasics(t *testing.T) {
	r := layoutOf(t, "| a | bb |\n|---|---|\n| ccc | d |", 1000)
	for _, s := range []string{"a", "bb", "ccc", "d"} {
		if _, ok := findText(r, s); !ok {
			t.Errorf("missing cell text %q", s)
		}
	}
	if h, _ := findText(r, "a"); h.Style&StyleBold == 0 {
		t.Error("header cell 'a' should be bold")
	}
	if b, _ := findText(r, "ccc"); b.Style&StyleBold != 0 {
		t.Error("body cell 'ccc' should not be bold")
	}
	if rects := opsOfKind(r, OpRect); len(rects) < 4 {
		t.Errorf("expected gridline + header rects, got %d", len(rects))
	}
}

func TestLayoutTableColumnWidth(t *testing.T) {
	// col0 holds a wide body cell; col1 must begin past col0's content width.
	r := layoutOf(t, "| a | b |\n|---|---|\n| wwwwww | x |", 1000)
	a, _ := findText(r, "a")
	b, _ := findText(r, "b")
	wideW := monoMeasure("wwwwww", DefaultLayoutStyle().BaseFontPx, 0, false)
	if b.X < a.X+wideW {
		t.Errorf("col1 start %v should be past col0 content (a.X=%v + width %v)", b.X, a.X, wideW)
	}
}

func TestLayoutTableAlignment(t *testing.T) {
	// Wide header makes the column wider than the body cell, so alignment shows.
	leftR := layoutOf(t, "| wwwwwwww |\n|:--|\n| v |", 1000)
	rightR := layoutOf(t, "| wwwwwwww |\n|--:|\n| v |", 1000)
	centerR := layoutOf(t, "| wwwwwwww |\n|:-:|\n| v |", 1000)
	lv, _ := findText(leftR, "v")
	rv, _ := findText(rightR, "v")
	cv, _ := findText(centerR, "v")
	if !(rv.X > cv.X && cv.X > lv.X) {
		t.Errorf("alignment x not ordered left<center<right: l=%v c=%v r=%v", lv.X, cv.X, rv.X)
	}
}

func TestLayoutTableCellWraps(t *testing.T) {
	r := layoutOf(t, "| h |\n|---|\n| aa bb cc dd ee ff |", 90)
	first, ok1 := findText(r, "aa")
	last, ok2 := findText(r, "ff")
	if !ok1 || !ok2 {
		t.Fatalf("missing wrapped cell words: aa=%v ff=%v", ok1, ok2)
	}
	if first.Y == last.Y {
		t.Errorf("narrow table cell did not wrap: aa and ff both at y=%v", first.Y)
	}
}

func TestLayoutCodeBlockHighlights(t *testing.T) {
	r := layoutOf(t, "```go\nfunc x() {}\n```", 400)
	found := false
	for _, op := range opsOfKind(r, OpText) {
		if op.Text == "func" && op.Color == ColorSynKeyword {
			found = true
		}
	}
	if !found {
		t.Error("go code block did not highlight 'func' as a keyword")
	}
	// A fence with no language stays plain.
	r2 := layoutOf(t, "```\nfunc x\n```", 400)
	for _, op := range opsOfKind(r2, OpText) {
		if op.Color == ColorSynKeyword {
			t.Errorf("unlanguaged code block should not highlight: %+v", op)
		}
	}
}
