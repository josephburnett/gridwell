package boundary

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// ratchetCounts maps each ledger row to the code that measures it. A row the
// ledger names and this map does not is a typo or a count deleted too early.
var ratchetCounts = map[string]func(t *testing.T, root string) int{
	"kind-switch":          length(kindSwitches),
	"unimplemented-embeds": length(unimplementedEmbeds),
	"copied-constants":     length(copiedConstants),
	"packages":             length(productionPackages),
	"lines":                productionLines,
}

func length(offenders func(*testing.T, string) []string) func(*testing.T, string) int {
	return func(t *testing.T, root string) int { return len(offenders(t, root)) }
}

type ratchetRow struct {
	line     int
	name     string
	recorded int
	floor    int
	ceiling  bool // no floor: the count only has to keep from rising
}

func readRatchet(t *testing.T, root string) []ratchetRow {
	t.Helper()
	f, err := os.Open(filepath.Join(root, "test", "boundary", "ratchet.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var rows []ratchetRow
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Split(text, "|")
		if len(fields) != 6 {
			t.Fatalf("ratchet.txt:%d: want 6 fields (name | recorded | floor | owner | mechanism | decision), got %d", n, len(fields))
		}
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		row := ratchetRow{line: n, name: fields[0]}
		if row.recorded, err = strconv.Atoi(fields[1]); err != nil {
			t.Fatalf("ratchet.txt:%d: recorded value %q is not a number", n, fields[1])
		}
		if fields[2] == "-" {
			row.ceiling = true
		} else if row.floor, err = strconv.Atoi(fields[2]); err != nil {
			t.Fatalf("ratchet.txt:%d: floor %q is neither a number nor -", n, fields[2])
		}
		for i, what := range []string{"owner", "mechanism", "decision"} {
			if fields[3+i] == "" {
				t.Errorf("ratchet.txt:%d: %s names no %s", n, row.name, what)
			}
		}
		rows = append(rows, row)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return rows
}

// TestRatchet holds the ledger exact: a rise is a regression, and a fall the
// ledger does not record would let the next commit rise back to the old value
// unseen, so both fail and a diff of ratchet.txt is the progress report.
func TestRatchet(t *testing.T) {
	root := repoRoot(t)
	seen := map[string]bool{}
	rows := readRatchet(t, root)
	for name := range ratchetCounts {
		if !slices.ContainsFunc(rows, func(r ratchetRow) bool { return r.name == name }) {
			t.Errorf("ratchet.txt names no row for the count %s: a count with no row is silent, which is the hole the ledger exists to close; add the row, or move the count to a permanent test of its own", name)
		}
	}
	for _, row := range rows {
		if seen[row.name] {
			t.Errorf("ratchet.txt:%d: %s is listed twice", row.line, row.name)
			continue
		}
		seen[row.name] = true
		count, ok := ratchetCounts[row.name]
		if !ok {
			t.Errorf("ratchet.txt:%d: %s names no count in ratchetCounts", row.line, row.name)
			continue
		}
		got := count(t, root)
		switch {
		case !row.ceiling && row.recorded == row.floor:
			t.Errorf("ratchet.txt:%d: %s is at its floor %d: delete the row and keep the count as a permanent test", row.line, row.name, row.floor)
		case got > row.recorded:
			t.Errorf("ratchet.txt:%d: %s rose from %d to %d: a count may only fall, by the mechanism the row names", row.line, row.name, row.recorded, got)
		case got < row.recorded:
			t.Errorf("ratchet.txt:%d: %s fell from %d to %d: record the new value in ratchet.txt in this commit", row.line, row.name, row.recorded, got)
		}
	}
}
