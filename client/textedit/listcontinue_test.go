package textedit

import (
	"strings"
	"testing"
	"unicode/utf16"
)

// apply runs a decision against the text, or a plain newline when there is
// none, as the textarea would.
func apply(text string, start, end int, e ListEdit, ok bool) (string, int) {
	u := utf16.Encode([]rune(text))
	if !ok {
		e = ListEdit{Start: start, End: end, Insert: "\n", Caret: start + 1}
	}
	out := append(append(append([]uint16{}, u[:e.Start]...), utf16.Encode([]rune(e.Insert))...), u[e.End:]...)
	return string(utf16.Decode(out)), e.Caret
}

func TestContinueList(t *testing.T) {
	cases := []struct {
		name string
		// text marks the caret with | and a selection with |...|.
		text  string
		shift bool
		mod   bool
		want  string // the result, caret marked with |
		ok    bool
	}{
		{"dash bullet", "- one|", false, false, "- one\n- |", true},
		{"star bullet", "* one|", false, false, "* one\n* |", true},
		{"plus bullet", "+ one|", false, false, "+ one\n+ |", true},
		{"wide spacing kept", "-   one|", false, false, "-   one\n-   |", true},
		{"unchecked box", "- [ ] buy milk|", false, false, "- [ ] buy milk\n- [ ] |", true},
		{"checked box continues unchecked", "- [x] done|", false, false, "- [x] done\n- [ ] |", true},
		{"capital checked box", "- [X] done|", false, false, "- [X] done\n- [ ] |", true},
		{"star box", "* [ ] a|", false, false, "* [ ] a\n* [ ] |", true},
		{"numbered", "1. first|", false, false, "1. first\n2. |", true},
		{"numbered paren", "9) ninth|", false, false, "9) ninth\n10) |", true},
		{"numbered box", "3. [x] c|", false, false, "3. [x] c\n4. [ ] |", true},
		{"nested spaces", "- a\n    - b|", false, false, "- a\n    - b\n    - |", true},
		{"tabs before marker", "\t\t- [ ] t|", false, false, "\t\t- [ ] t\n\t\t- [ ] |", true},
		{"mixed tab and space", " \t* x|", false, false, " \t* x\n \t* |", true},
		{"caret mid item splits it", "- foo|bar", false, false, "- foo\n- |bar", true},
		{"middle line of list", "- a|\n- b", false, false, "- a\n- |\n- b", true},
		{"selection replaced", "- a|bc|d", false, false, "- a\n- |d", true},
		{"selection across lines", "- a|b\nc|d", false, false, "- a\n- |d", true},
		{"non-ascii before caret", "- é😀|", false, false, "- é😀\n- |", true},
		{"box glued to text is body", "- [ ]x|", false, false, "- [ ]x\n- |", true},

		{"empty bullet ends list", "- a\n- |", false, false, "- a\n|", true},
		{"empty box ends list", "- [ ] a\n- [ ] |", false, false, "- [ ] a\n|", true},
		{"empty box without trailing space", "- [ ]|", false, false, "|", true},
		{"empty number ends list", "1. a\n2. |", false, false, "1. a\n|", true},
		{"empty nested item drops its indent too", "- a\n\t- |", false, false, "- a\n|", true},
		{"marker with trailing blanks only", "- a\n-   |  ", false, false, "- a\n|", true},
		{"empty item mid list", "- |\n- b", false, false, "|\n- b", true},

		{"shift enter is plain", "- one|", true, false, "- one\n|", false},
		{"modifier is plain", "- one|", false, true, "- one\n|", false},
		{"plain line", "hello|", false, false, "hello\n|", false},
		{"dash without space", "-one|", false, false, "-one\n|", false},
		{"caret inside the marker", "-| one", false, false, "-\n| one", false},
		{"caret in indentation", "  |- one", false, false, "  \n|- one", false},
		{"thematic break", "* * *|", false, false, "* * *\n|", false},
		{"dash break", "- - -|", false, false, "- - -\n|", false},
		{"number without space", "1.5|", false, false, "1.5\n|", false},
	}
	for _, c := range cases {
		parts := strings.Split(c.text, "|")
		text := strings.Join(parts, "")
		start := len(utf16.Encode([]rune(parts[0])))
		end := start
		if len(parts) == 3 {
			end = start + len(utf16.Encode([]rune(parts[1])))
		}
		e, ok := ContinueList(ListEnter{Text: text, SelStart: start, SelEnd: end, Shift: c.shift, OtherMod: c.mod})
		if ok != c.ok {
			t.Errorf("%s: ok = %v, want %v", c.name, ok, c.ok)
			continue
		}
		got, caret := apply(text, start, end, e, ok)
		u := utf16.Encode([]rune(got))
		marked := string(utf16.Decode(u[:caret])) + "|" + string(utf16.Decode(u[caret:]))
		if marked != c.want {
			t.Errorf("%s: got %q, want %q", c.name, marked, c.want)
		}
	}
}

func TestContinueListComposingIsNative(t *testing.T) {
	if _, ok := ContinueList(ListEnter{Text: "- a", SelStart: 3, SelEnd: 3, Composing: true}); ok {
		t.Fatal("Enter that commits an IME composition must stay native")
	}
}
