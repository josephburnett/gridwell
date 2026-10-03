package textedit

import (
	"strconv"
	"strings"
	"unicode/utf16"
)

// ListEnter is an Enter keydown in the textarea. Offsets are UTF-16 code
// units, as selectionStart counts them.
type ListEnter struct {
	Text             string
	SelStart, SelEnd int
	Shift            bool
	// OtherMod is ctrl, alt or meta held; Composing is an IME composition.
	OtherMod, Composing bool
}

// ListEdit replaces [Start, End) with Insert and puts the caret at Caret, all
// in UTF-16 code units.
type ListEdit struct {
	Start, End int
	Insert     string
	Caret      int
}

// ContinueList decides what Enter does on a markdown list line: the new line
// starts with the same indentation and marker (the next number, an unchecked
// box), and Enter on a line holding only its marker removes the marker. False
// means Enter stays the textarea's own plain newline, as Shift+Enter always
// does.
func ContinueList(in ListEnter) (ListEdit, bool) {
	if in.Shift || in.OtherMod || in.Composing {
		return ListEdit{}, false
	}
	u := utf16.Encode([]rune(in.Text))
	start, end := clampSel(in.SelStart, len(u)), clampSel(in.SelEnd, len(u))
	if start > end {
		start, end = end, start
	}
	ls := start
	for ls > 0 && u[ls-1] != '\n' {
		ls--
	}
	le := start
	for le < len(u) && u[le] != '\n' {
		le++
	}
	line := string(utf16.Decode(u[ls:le]))
	indent, marker, next, ok := parseListMarker(line)
	// The prefix is ASCII, so its length in bytes is its length in units.
	if !ok || start <= ls+len(indent) {
		return ListEdit{}, false
	}
	if start == end && strings.TrimRight(line[len(indent)+len(marker):], " \t") == "" {
		return ListEdit{Start: ls, End: le, Caret: ls}, true
	}
	if start < ls+len(indent)+len(marker) {
		return ListEdit{}, false
	}
	insert := "\n" + indent + next
	return ListEdit{Start: start, End: end, Insert: insert, Caret: start + len(insert)}, true
}

func clampSel(off, n int) int {
	return min(max(off, 0), n)
}

// parseListMarker splits a list line into its leading whitespace, its marker
// as written (spacing included), and the marker the next item starts with.
func parseListMarker(line string) (indent, marker, next string, ok bool) {
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	indent, rest := line[:i], line[i:]
	if isThematicBreak(rest) {
		return "", "", "", false
	}
	var j int
	switch {
	case len(rest) >= 2 && strings.IndexByte("-*+", rest[0]) >= 0 && rest[1] == ' ':
		j, next = 1, rest[:1]
	default:
		d := 0
		for d < len(rest) && d < 9 && rest[d] >= '0' && rest[d] <= '9' {
			d++
		}
		if d == 0 || d+1 >= len(rest) || (rest[d] != '.' && rest[d] != ')') || rest[d+1] != ' ' {
			return "", "", "", false
		}
		n, _ := strconv.Atoi(rest[:d])
		j, next = d+1, strconv.Itoa(n+1)+rest[d:d+1]
	}
	k := j
	for k < len(rest) && rest[k] == ' ' {
		k++
	}
	next += rest[j:k]
	if box := rest[k:]; len(box) >= 3 && box[0] == '[' && box[2] == ']' && strings.IndexByte(" xX", box[1]) >= 0 &&
		(len(box) == 3 || box[3] == ' ') {
		m := k + 3
		for m < len(rest) && rest[m] == ' ' {
			m++
		}
		gap := rest[k+3 : m]
		if gap == "" {
			gap = " "
		}
		next += "[ ]" + gap
		k = m
	}
	return indent, rest[:k], next, true
}

// isThematicBreak reports a markdown rule such as "* * *", which reads as a
// bullet but is not one.
func isThematicBreak(s string) bool {
	var c byte
	n := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ', '\t':
		case '-', '*', '_':
			if c != 0 && s[i] != c {
				return false
			}
			c = s[i]
			n++
		default:
			return false
		}
	}
	return n >= 3
}
