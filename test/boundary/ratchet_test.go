package boundary

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// ratchetCounts maps each ledger row to the code that measures it. A row the
// ledger names and this map does not is a typo or a count deleted too early.
var ratchetCounts = map[string]func(t *testing.T, root string) int{
	"packages": length(productionPackages),
	"lines":    productionLines,
	"debt":     debtCount,
	"axes":     axesUncovered,
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
	return parseRatchet(t, f)
}

func parseRatchet(t *testing.T, r io.Reader) []ratchetRow {
	t.Helper()
	var rows []ratchetRow
	var err error
	sc := bufio.NewScanner(r)
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

// raisedAboveCommitted names each row recorded above the value the committed
// ledger holds for it. A row new since then compares to nothing, and a row
// gone since then reached its floor. A row counted in lines may rise by
// exactly the debt entered since then (debt.txt).
func raisedAboveCommitted(committed, now []ratchetRow, entered int) []string {
	was := map[string]int{}
	for _, r := range committed {
		was[r.name] = r.recorded
	}
	var out []string
	for _, r := range now {
		prev, ok := was[r.name]
		switch {
		case !ok || r.recorded <= prev:
		case !slices.Contains(inLines, r.name):
			out = append(out, fmt.Sprintf("ratchet.txt:%d: %s is recorded at %d, above the %d committed before it: a count only falls", r.line, r.name, r.recorded, prev))
		case r.recorded-prev != entered:
			out = append(out, fmt.Sprintf("ratchet.txt:%d: %s rose by %d with no debt entry naming the decision", r.line, r.name, r.recorded-prev))
		}
	}
	return out
}

// committedRatchet reads the ledger as HEAD holds it; ok is false when HEAD
// has none.
func committedRatchet(t *testing.T, root string) (rows []ratchetRow, ok bool) {
	t.Helper()
	out, ok := committedFile(t, root, "test/boundary/ratchet.txt")
	if !ok {
		return nil, false
	}
	return parseRatchet(t, bytes.NewReader(out)), true
}

// committedFile is rel as HEAD holds it; ok is false when HEAD has none, and
// the caller is skipped when git cannot answer.
func committedFile(t *testing.T, root, rel string) ([]byte, bool) {
	t.Helper()
	run := func(args ...string) ([]byte, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		return cmd.Output()
	}
	if _, err := run("rev-parse", "--verify", "HEAD"); err != nil {
		t.Skipf("git cannot read HEAD here, so the committed ledger is not compared: %v", err)
	}
	path := "HEAD:" + rel
	if _, err := run("cat-file", "-e", path); err != nil {
		return nil, false
	}
	out, err := run("show", path)
	if err != nil {
		t.Skipf("git show %s: %v", path, err)
	}
	return out, true
}

func TestRaisedAboveCommitted(t *testing.T) {
	committed := parseRatchet(t, strings.NewReader("# header\nlines | 100 | - | o | m | d\ngone | 3 | 2 | o | m | d\n"))
	now := parseRatchet(t, strings.NewReader("# header\nlines | 101 | - | o | m | d\nnew | 9 | 0 | o | m | d\n"))
	want := []string{"ratchet.txt:2: lines rose by 1 with no debt entry naming the decision"}
	if got := raisedAboveCommitted(committed, now, 0); !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := raisedAboveCommitted(now, committed, 0); len(got) != 0 {
		t.Errorf("a fall passes, got %q", got)
	}
}

// TestRatchet holds the ledger exact: a rise is a regression, and a fall the
// ledger does not record would let the next commit rise back to the old value
// unseen, so both fail and a diff of ratchet.txt is the progress report. The
// value HEAD recorded is the ceiling, so a rise with the ledger edited to
// match it fails too.
func TestRatchet(t *testing.T) {
	root := repoRoot(t)
	seen := map[string]bool{}
	rows := readRatchet(t, root)
	t.Run("committed", func(t *testing.T) {
		committed, ok := committedRatchet(t, root)
		if !ok {
			return
		}
		was, _ := committedFile(t, root, debtFile)
		entered := parseDebt(t, bytes.NewReader(was)).enteredSince(readDebt(t, root))
		for _, msg := range raisedAboveCommitted(committed, rows, entered) {
			t.Error(msg)
		}
	})
	owing := readDebt(t, root).lowWater != 0
	for name := range ratchetCounts {
		if name == "debt" && !owing {
			continue
		}
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
