package boundary

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// debtFile is where a decision that raised a ceiling enters the rise.
const debtFile = "test/boundary/debt.txt"

// inLines is the ledger rows counted in lines, the ones a debt entry may raise.
var inLines = []string{"lines", "debt"}

// debtLedger is debt.txt: the lines value before the first rise, 0 when
// nothing is owed, and each rise as written, with its amount.
type debtLedger struct {
	lowWater int
	entries  map[string]int
}

func parseDebt(t *testing.T, r io.Reader) debtLedger {
	t.Helper()
	d := debtLedger{entries: map[string]int{}}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Split(text, "|")
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		var err error
		switch {
		case len(fields) == 2 && fields[0] == "low-water":
			d.lowWater, err = strconv.Atoi(fields[1])
		case len(fields) == 3 && fields[1] != "":
			d.entries[text], err = strconv.Atoi(fields[2])
		default:
			t.Fatalf("debt.txt:%d: want low-water | lines, or date | decision | amount", n)
		}
		if err != nil {
			t.Fatalf("debt.txt:%d: %v", n, err)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return d
}

func readDebt(t *testing.T, root string) debtLedger {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(debtFile)))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return parseDebt(t, strings.NewReader(string(b)))
}

// enteredSince is the debt now enters that d does not: the rise a commit may
// record.
func (d debtLedger) enteredSince(now debtLedger) int {
	sum := 0
	for e, amount := range now.entries {
		if _, ok := d.entries[e]; !ok {
			sum += amount
		}
	}
	return sum
}

// debtCount is the lines above the low-water mark, 0 when nothing is owed.
func debtCount(t *testing.T, root string) int {
	d := readDebt(t, root)
	if d.lowWater == 0 {
		return 0
	}
	return max(0, productionLines(t, root)-d.lowWater)
}

func TestARiseNeedsADebtEntry(t *testing.T) {
	committed := parseRatchet(t, strings.NewReader("lines | 100 | - | o | m | d\npackages | 5 | - | o | m | d\n"))
	now := parseRatchet(t, strings.NewReader("lines | 107 | - | o | m | d\npackages | 5 | - | o | m | d\ndebt | 7 | 0 | o | m | d\n"))
	was := parseDebt(t, strings.NewReader("# header\n"))
	owes := parseDebt(t, strings.NewReader("# header\nlow-water | 100\n2026-10-22 | a decision | 7\n"))
	if got := raisedAboveCommitted(committed, now, was.enteredSince(owes)); len(got) != 0 {
		t.Errorf("a rise the debt enters passes, got %q", got)
	}
	want := []string{"ratchet.txt:1: lines rose by 7 with no debt entry naming the decision"}
	if got := raisedAboveCommitted(committed, now, owes.enteredSince(owes)); !slices.Equal(got, want) {
		t.Errorf("a rise with no entry: got %q, want %q", got, want)
	}
	short := parseDebt(t, strings.NewReader("low-water | 100\n2026-10-22 | a decision | 6\n"))
	if got := raisedAboveCommitted(committed, now, was.enteredSince(short)); !slices.Equal(got, want) {
		t.Errorf("an entry short of the rise: got %q, want %q", got, want)
	}
	more := parseRatchet(t, strings.NewReader("lines | 100 | - | o | m | d\npackages | 6 | - | o | m | d\n"))
	if got := raisedAboveCommitted(committed, more, 1); len(got) != 1 || !strings.Contains(got[0], "a count only falls") {
		t.Errorf("a debt is in lines and raises no other row, got %q", got)
	}
}
