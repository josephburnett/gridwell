package markdown

import "testing"

// monoMeasure is a deterministic measure: every rune is half the font size
// wide, regardless of style. Makes layout geometry exactly assertable.
func monoMeasure(text string, fontPx float64, _ SpanStyle, _ bool) float64 {
	return float64(len([]rune(text))) * fontPx * 0.5
}

// styleEmbed treats any StyleEmbed span as an embed (the wasm renderer also
// catches tile-link hrefs via embed.SpanIsEmbed, but StyleEmbed suffices here).
func styleEmbed(sp Span) bool { return sp.Style&StyleEmbed != 0 }

func layoutOf(t *testing.T, src string, width float64) LayoutResult {
	t.Helper()
	return Layout(Lower([]byte(src)), monoMeasure, styleEmbed, width, DefaultLayoutStyle())
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

func TestLayoutEmbedIsAtomic(t *testing.T) {
	r := layoutOf(t, "before ![alt](pic.png) after", 1000)
	embeds := opsOfKind(r, OpEmbed)
	if len(embeds) != 1 {
		t.Fatalf("want 1 embed op, got %d", len(embeds))
	}
	e := embeds[0]
	if e.W != DefaultLayoutStyle().EmbedW || e.H != DefaultLayoutStyle().EmbedH {
		t.Errorf("embed size = %vx%v, want default", e.W, e.H)
	}
	// Surrounding text still renders.
	if len(opsOfKind(r, OpText)) == 0 {
		t.Error("expected surrounding text ops alongside the embed")
	}
}

func TestLayoutThematicBreak(t *testing.T) {
	r := layoutOf(t, "a\n\n---\n\nb", 400)
	if rules := opsOfKind(r, OpRule); len(rules) != 1 {
		t.Errorf("want 1 rule op, got %d", len(rules))
	}
}
