package textcursor

import (
	"math"
	"strings"
	"unicode/utf16"

	"github.com/josephburnett/gridwell/client/markdown"
)

// Grid is the painter's layout of raw text, in pixels from the document's
// top-left: rows Slot apart from Top, columns Adv apart from Left, soft-wrapped
// at Cols.
type Grid struct {
	Cols                 int
	Left, Top, Slot, Adv float64
}

// CaretAt returns the textarea offset, in UTF-16 code units as
// setSelectionRange counts, of the caret a press at (x, y) puts down in src as
// g lays it out. The wrap is markdown.WrapRawLine's, the one the painter and
// the textarea share. A point before a row's start or above the first row
// clamps to the start; past a row's end, to its end; below the last row, to
// the end of src.
func CaretAt(src string, g Grid, x, y float64) int {
	row := 0
	if g.Slot > 0 {
		row = max(int(math.Floor((y-g.Top)/g.Slot)), 0)
	}
	col := 0
	if g.Adv > 0 {
		col = max(int(math.Round((x-g.Left)/g.Adv)), 0)
	}
	lines := strings.Split(src, "\n")
	off := 0
	for i, line := range lines {
		pieces := markdown.WrapRawLine(line, g.Cols)
		for j, piece := range pieces {
			r := []rune(piece)
			if last := i == len(lines)-1 && j == len(pieces)-1; row > 0 && last {
				col = len(r)
			} else if row > 0 {
				row--
				off += utf16Len(r)
				continue
			}
			return off + utf16Len(r[:min(col, len(r))])
		}
		off++ // the '\n'
	}
	return off
}

func utf16Len(r []rune) int { return len(utf16.Encode(r)) }
