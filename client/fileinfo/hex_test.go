package fileinfo

import (
	"strings"
	"testing"
)

func TestHexLines(t *testing.T) {
	for _, c := range []struct {
		name     string
		buf      string
		from, to int
		want     []HexLine
	}{
		{"ascii and a short last row", "Hello, world!\n\x00\xffAB", 0, 99, []HexLine{
			{"00000000", "48 65 6c 6c 6f 2c 20 77  6f 72 6c 64 21 0a 00 ff", "Hello, world!..."},
			{"00000010", "41 42                                           ", "AB"},
		}},
		{"a rune at its first byte", "h\xc3\xa9\xe2\x82\xac!", 0, 16, []HexLine{
			{"00000000", "68 c3 a9 e2 82 ac 21                            ", "hé€!"},
		}},
		{"a rune across rows", strings.Repeat("a", 15) + "\xc3\xa9", 0, 32, []HexLine{
			{"00000000", "61 61 61 61 61 61 61 61  61 61 61 61 61 61 61 c3", strings.Repeat("a", 15) + "é"},
			{"00000010", "a9                                              ", ""},
		}},
		{"invalid and cut", "\x80\xc3(\xe2\x82", 0, 16, []HexLine{
			{"00000000", "80 c3 28 e2 82                                  ", "..(.."},
		}},
		{"a window", strings.Repeat("x", 48), 17, 20, []HexLine{
			{"00000010", "78 78 78 78 78 78 78 78  78 78 78 78 78 78 78 78", strings.Repeat("x", 16)},
		}},
	} {
		got := HexLines([]byte(c.buf), c.from, c.to)
		if len(got) != len(c.want) {
			t.Errorf("%s: %d lines, want %d: %q", c.name, len(got), len(c.want), got)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s line %d = %q, want %q", c.name, i, got[i], c.want[i])
			}
		}
	}
}

func TestPaging(t *testing.T) {
	if h, ok := NextPage(0, 100_000); !ok || h != "bytes=0-65535" {
		t.Errorf("first page = %q %v", h, ok)
	}
	if h, ok := NextPage(PageBytes, 100_000); !ok || h != "bytes=65536-99999" {
		t.Errorf("last page = %q %v", h, ok)
	}
	if _, ok := NextPage(100_000, 100_000); ok {
		t.Error("a loaded file asks again")
	}
	if !WantMore(800, 200, 1100) || WantMore(0, 200, 1100) {
		t.Error("WantMore asks for the next page only within a screen of the end")
	}
	if got := HexHeader(65536, 100_000); got != "offset 0 to 65536 of 100000 bytes" {
		t.Errorf("Header = %q", got)
	}
}

func TestRangeSize(t *testing.T) {
	for h, want := range map[string]int64{"bytes 0-65535/100000": 100000, "bytes */0": 0} {
		if n, ok := RangeSize(h); !ok || n != want {
			t.Errorf("RangeSize(%q) = %d %v", h, n, ok)
		}
	}
	if _, ok := RangeSize("bytes 0-1/*"); ok {
		t.Error("an unknown size is a size")
	}
}
