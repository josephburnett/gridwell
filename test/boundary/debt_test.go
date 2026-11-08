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

// ceilings are the ledger rows a debt entry may raise; the debt row counts
// what they owe and rises with them.
var ceilings = []string{"lines", "packages"}

// raisable is a row a debt entry raises.
func raisable(row string) bool { return row == "debt" || slices.Contains(ceilings, row) }

// debtLedger is debt.txt: each ceiling's value before its first rise, none
// when nothing is owed, and each rise as written, with its amount and the
// ceiling it raised.
type debtLedger struct {
	lowWater map[string]int
	entries  map[string]debtEntry
}

type debtEntry struct {
	amount  int
	ceiling string
}

func parseDebt(t *testing.T, r io.Reader) debtLedger {
	t.Helper()
	d := debtLedger{lowWater: map[string]int{}, entries: map[string]debtEntry{}}
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
		if len(fields) == 2 && fields[0] == "low-water" {
			fields = []string{"low-water", "lines", fields[1]}
		} else if len(fields) == 3 && fields[0] != "low-water" {
			fields = append(fields, "lines")
		}
		var err error
		switch {
		case len(fields) == 3 && fields[0] == "low-water" && slices.Contains(ceilings, fields[1]):
			d.lowWater[fields[1]], err = strconv.Atoi(fields[2])
		case len(fields) == 4 && fields[1] != "" && slices.Contains(ceilings, fields[3]):
			e := debtEntry{ceiling: fields[3]}
			e.amount, err = strconv.Atoi(fields[2])
			d.entries[text] = e
		default:
			t.Fatalf("debt.txt:%d: want low-water | lines, low-water | packages | count, date | decision | amount, or date | decision | amount | packages", n)
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

// enteredSince is the debt now enters that d does not, per row it raises:
// the rise a commit may record.
func (d debtLedger) enteredSince(now debtLedger) map[string]int {
	out := map[string]int{}
	for e, entry := range now.entries {
		if _, ok := d.entries[e]; !ok {
			out[entry.ceiling] += entry.amount
			out["debt"] += entry.amount
		}
	}
	return out
}

// owing is whether any ceiling stands above its low-water mark.
func (d debtLedger) owing() bool { return len(d.lowWater) > 0 }

// debtCount is the lines and packages above their low-water marks, 0 when
// nothing is owed.
func debtCount(t *testing.T, root string) int {
	d := readDebt(t, root)
	n := 0
	if lw, ok := d.lowWater["lines"]; ok {
		n += max(0, productionLines(t, root)-lw)
	}
	if lw, ok := d.lowWater["packages"]; ok {
		n += max(0, len(productionPackages(t, root))-lw)
	}
	return n
}

func TestARiseNeedsADebtEntry(t *testing.T) {
	committed := parseRatchet(t, strings.NewReader("lines | 100 | - | o | m | d\npackages | 5 | - | o | m | d\nrow | 3 | 0 | o | m | d\n"))
	now := parseRatchet(t, strings.NewReader("lines | 107 | - | o | m | d\npackages | 5 | - | o | m | d\nrow | 3 | 0 | o | m | d\ndebt | 7 | 0 | o | m | d\n"))
	was := parseDebt(t, strings.NewReader("# header\n"))
	owes := parseDebt(t, strings.NewReader("# header\nlow-water | 100\n2026-10-22 | a decision | 7\n"))
	if got := raisedAboveCommitted(committed, now, was.enteredSince(owes)); len(got) != 0 {
		t.Errorf("a rise the debt enters passes, got %q", got)
	}
	want := []string{"ratchet.txt:1: lines rose by 7 with no debt entry naming the decision"}
	if got := raisedAboveCommitted(committed, now, owes.enteredSince(owes)); !slices.Equal(got[:1], want) {
		t.Errorf("a rise with no entry: got %q, want %q", got, want)
	}
	short := parseDebt(t, strings.NewReader("low-water | 100\n2026-10-22 | a decision | 6\n"))
	if got := raisedAboveCommitted(committed, now, was.enteredSince(short)); !slices.Equal(got[:1], want) {
		t.Errorf("an entry short of the rise: got %q, want %q", got, want)
	}
	more := parseRatchet(t, strings.NewReader("lines | 100 | - | o | m | d\npackages | 6 | - | o | m | d\nrow | 4 | 0 | o | m | d\ndebt | 1 | 0 | o | m | d\n"))
	got := raisedAboveCommitted(committed, more, was.enteredSince(owes))
	if len(got) != 2 || !strings.Contains(got[0], "packages rose by 1 with no debt entry") ||
		!strings.Contains(got[1], "row is recorded at 4") || !strings.Contains(got[1], "a count only falls") {
		t.Errorf("a lines entry raises no other ceiling and no class row, got %q", got)
	}
	pkg := parseDebt(t, strings.NewReader("low-water | packages | 5\n2026-11-07 | a decision | 1 | packages\n"))
	if got := raisedAboveCommitted(committed, more, was.enteredSince(pkg)); len(got) != 1 || !strings.Contains(got[0], "row is recorded") {
		t.Errorf("a packages entry raises packages and the debt, got %q", got)
	}
}
