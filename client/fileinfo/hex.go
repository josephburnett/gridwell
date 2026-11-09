package fileinfo

// The hex view: a file's bytes per row of 16, the offset, the bytes in hex
// and their UTF-8 decoding, and the pages it asks the content API for, one
// byte range at a time.

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// RowBytes is the bytes one row shows.
const RowBytes = 16

// PageBytes is the bytes one ask loads; a multiple of RowBytes, so a page
// ends on a row.
const PageBytes = 64 << 10

// HexLine is one row of the hex view.
type HexLine struct {
	Offset, Hex, Text string
}

// HexLines lays out the rows of buf, the file's bytes from its start, that
// begin in [from, to). A rune is shown at its first byte and its other
// bytes as nothing; a byte that starts no valid rune, or one that does not
// print, shows as '.'. A rune cut by the end of buf shows as '.' until more
// of the file is loaded, so the view lays its row out again then.
func HexLines(buf []byte, from, to int) []HexLine {
	var out []HexLine
	for row := from - from%RowBytes; row < min(to, len(buf)); row += RowBytes {
		end := min(row+RowBytes, len(buf))
		var hex, text strings.Builder
		for i := row; i < row+RowBytes; i++ {
			if i > row {
				hex.WriteByte(' ')
			}
			if i == row+RowBytes/2 {
				hex.WriteByte(' ')
			}
			if i >= end {
				hex.WriteString("  ")
				continue
			}
			fmt.Fprintf(&hex, "%02x", buf[i])
			text.WriteString(glyph(buf, i))
		}
		out = append(out, HexLine{Offset: fmt.Sprintf("%08x", row), Hex: hex.String(), Text: text.String()})
	}
	return out
}

// glyph is what the byte at i shows in the text column.
func glyph(buf []byte, i int) string {
	if continuation(buf, i) {
		return ""
	}
	r, n := utf8.DecodeRune(buf[i:])
	if r == utf8.RuneError && n <= 1 || !unicode.IsPrint(r) {
		return "."
	}
	return string(r)
}

// continuation is a byte inside a valid rune that starts before it.
func continuation(buf []byte, i int) bool {
	if utf8.RuneStart(buf[i]) {
		return false
	}
	for start := i - 1; start >= max(0, i-utf8.UTFMax+1); start-- {
		if utf8.RuneStart(buf[start]) {
			r, n := utf8.DecodeRune(buf[start:])
			return !(r == utf8.RuneError && n <= 1) && start+n > i
		}
	}
	return false
}

// NextPage is the Range header asking the page after loaded bytes of a file of
// size, ok false once it is all loaded.
func NextPage(loaded, size int64) (header string, ok bool) {
	if loaded >= size {
		return "", false
	}
	return "bytes=" + strconv.FormatInt(loaded, 10) + "-" + strconv.FormatInt(min(loaded+PageBytes, size)-1, 10), true
}

// WantMore says a view scrolled to top, showing view of content pixels, is
// within a screen of its end, so the next page is asked for.
func WantMore(top, view, content float64) bool { return top+2*view >= content }

// HexHeader is the hex view's first line: the offsets shown, of the file's size.
func HexHeader(loaded, size int64) string {
	return fmt.Sprintf("offset 0 to %d of %d bytes", loaded, size)
}

// RangeSize is the file's size a Content-Range header names, "bytes a-b/size" or
// "bytes */size".
func RangeSize(contentRange string) (int64, bool) {
	i := strings.LastIndexByte(contentRange, '/')
	if i < 0 {
		return 0, false
	}
	n, err := strconv.ParseInt(contentRange[i+1:], 10, 64)
	return n, err == nil && n >= 0
}
