package node

// The conversion is an upgrade path that runs once, on the user's real data,
// with no undo — so the interesting question is not "does it fold correctly"
// (convert_test.go) but "what does a kill halfway through leave behind". It is
// killed for real: Electron's sidecar wrapper SIGTERMs a server that has not
// announced itself, and a fold of a real home takes longer than that wrapper
// used to wait.
//
// These tests stop the conversion after each of its steps and assert the two
// halves of the crash contract: gridwell.db never exists until the whole
// conversion has succeeded, so a killed attempt is always retryable from the
// untouched db/; and the one window where it does exist beside db/ — between
// the two renames that commit it — is finished by ensureStore rather than
// converted again.

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/josephburnett/gridwell/internal/config"
)

// errKilled stands in for the SIGTERM.
var errKilled = errors.New("killed")

// killAfter aborts a conversion once the named step has run.
func killAfter(step string) func(string) error {
	return func(s string) error {
		if s == step {
			return errKilled
		}
		return nil
	}
}

// TestConvertPublishesNothingUntilItHasFinished kills the conversion after
// every step it has and asserts that gridwell.db — the file the next boot
// opens — never appeared, that the old layout it was built from is untouched,
// and that a retry then produces exactly the content an uninterrupted
// conversion produces.
func TestConvertPublishesNothingUntilItHasFinished(t *testing.T) {
	want := cleanConversion(t)
	for _, step := range []string{stepSnapshot, stepIdentity, stepPlugin + "plug1", stepReferences, stepConnections} {
		t.Run(step, func(t *testing.T) {
			f := buildLegacyHome(t)
			if err := convert(f.home, f.cfg, killAfter(step)); !errors.Is(err, errKilled) {
				t.Fatalf("convert killed after %s: err = %v, want the kill", step, err)
			}
			// The next boot must not see a store: a half-converted
			// gridwell.db either opens silently on partial data or refuses
			// with a misleading identity error, and nothing self-heals.
			if _, err := os.Stat(config.DBFile(f.home)); !os.IsNotExist(err) {
				t.Fatalf("a kill after %s published %s (stat err = %v)", step, config.DBFile(f.home), err)
			}
			// The source of truth is still there to retry from.
			if _, err := os.Stat(filepath.Join(f.home, "db", f.nodeID, "store.db")); err != nil {
				t.Fatalf("the old layout must survive a killed attempt: %v", err)
			}
			if _, err := os.Stat(filepath.Join(f.home, "db.pre-one-node")); !os.IsNotExist(err) {
				t.Fatal("nothing is set aside until the conversion has committed")
			}
			// And the retry is clean, leftover temp and all.
			if err := Convert(f.home, f.cfg); err != nil {
				t.Fatalf("retry after a kill at %s: %v", step, err)
			}
			if got := dumpStore(t, config.DBFile(f.home)); got != want {
				t.Errorf("retry after a kill at %s converted differently:\n%s", step, diff(want, got))
			}
			if _, err := os.Stat(config.DBFile(f.home) + ".converting"); !os.IsNotExist(err) {
				t.Error("the temp must not survive a successful conversion")
			}
		})
	}
}

// TestAKillBetweenTheRenamesIsFinishedNotRedone covers the one window where
// gridwell.db and db/ both exist: the store is complete and only the set-aside
// is missing, so the next boot finishes that rename. Converting a second time
// over data the first fold already folded is the failure this pins shut.
func TestAKillBetweenTheRenamesIsFinishedNotRedone(t *testing.T) {
	want := cleanConversion(t)
	f := buildLegacyHome(t)
	if err := convert(f.home, f.cfg, killAfter(stepCommitted)); !errors.Is(err, errKilled) {
		t.Fatalf("convert killed after the commit: err = %v, want the kill", err)
	}
	if _, err := os.Stat(config.DBFile(f.home)); err != nil {
		t.Fatalf("the commit rename must have landed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.home, "db")); err != nil {
		t.Fatalf("db/ is still to be set aside: %v", err)
	}

	// The boot path: identity verified, the set-aside finished, no second
	// conversion.
	if err := ensureStore(f.home, f.cfg); err != nil {
		t.Fatalf("boot after a kill between the renames: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.home, "db")); !os.IsNotExist(err) {
		t.Error("the interrupted set-aside was never finished")
	}
	if _, err := os.Stat(filepath.Join(f.home, "db.pre-one-node", f.nodeID, "store.db")); err != nil {
		t.Errorf("the old files must survive beside, not vanish: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.home, "cache")); !os.IsNotExist(err) {
		t.Error("the disposable cache goes with the set-aside")
	}
	if got := dumpStore(t, config.DBFile(f.home)); got != want {
		t.Errorf("finishing the rename changed the data:\n%s", diff(want, got))
	}
	// A second boot is a no-op, and the finished store still verifies.
	if err := ensureStore(f.home, f.cfg); err != nil {
		t.Fatalf("second boot: %v", err)
	}
	if got := dumpStore(t, config.DBFile(f.home)); got != want {
		t.Errorf("a second boot changed the data:\n%s", diff(want, got))
	}
}

// cleanConversion is the reference: the same fixture converted with no
// interruption. Every killed-and-retried conversion must land on it exactly.
func cleanConversion(t *testing.T) string {
	t.Helper()
	f := buildLegacyHome(t)
	if err := Convert(f.home, f.cfg); err != nil {
		t.Fatalf("reference conversion: %v", err)
	}
	return dumpStore(t, config.DBFile(f.home))
}

// minted names the columns a conversion mints fresh on every run — a random
// object_id, a wall-clock stamp. They carry no user content, so comparing
// content across two runs means comparing everything else. mintedRows is the
// same for a whole row: the store's own random per-file uuid.
var (
	minted     = map[string]bool{"object_id": true, "created_at": true, "updated_at": true}
	mintedRows = map[string]bool{"system:plugin_uuid": true}
)

// dumpStore renders every row of every table, ordered, as text: what the user
// has, independent of the file bytes (which differ run to run — a VACUUM INTO
// of a WAL file is not reproducible).
func dumpStore(t *testing.T, path string) string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var tables []string
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	rows.Close()

	var out strings.Builder
	for _, table := range tables {
		cols := tableColumns(t, db, table)
		var keep []string
		for _, c := range cols {
			if !minted[c] {
				keep = append(keep, c)
			}
		}
		if len(keep) == 0 {
			continue
		}
		fmt.Fprintf(&out, "== %s (%s)\n", table, strings.Join(keep, ", "))
		order := make([]string, len(keep))
		for i := range keep {
			order[i] = strconv.Itoa(i + 1)
		}
		q := `SELECT "` + strings.Join(keep, `", "`) + `" FROM "` + table + `" ORDER BY ` + strings.Join(order, ", ")
		drows, err := db.Query(q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		for drows.Next() {
			cells := make([]any, len(keep))
			ptrs := make([]any, len(keep))
			for i := range cells {
				ptrs[i] = &cells[i]
			}
			if err := drows.Scan(ptrs...); err != nil {
				drows.Close()
				t.Fatal(err)
			}
			parts := make([]string, len(cells))
			for i, c := range cells {
				parts[i] = cell(c)
			}
			if mintedRows[table+":"+parts[0]] {
				continue
			}
			out.WriteString("  " + strings.Join(parts, " | ") + "\n")
		}
		drows.Close()
	}
	return out.String()
}

func tableColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, n)
	}
	return cols
}

// cell renders one value: blobs as their text when they are text (a pane
// layout, a markdown note), so a difference is readable.
func cell(v any) string {
	b, ok := v.([]byte)
	if !ok {
		return fmt.Sprintf("%v", v)
	}
	if utf8.Valid(b) {
		return string(b)
	}
	return fmt.Sprintf("%x", b)
}

// diff reports the first differing line, which is enough to name what moved.
func diff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			return fmt.Sprintf("line %d:\n  want: %s\n  got:  %s", i+1, wl, gl)
		}
	}
	return "(identical)"
}

// TestHeartbeatKeepsALongStepAudible is the other half of the kill: the
// wrapper spares a sidecar that is still talking, so a conversion that spends
// minutes inside one VACUUM or one migration chain must say so. Nothing in the
// conversion itself reports progress, and the tick is what stands in for it.
func TestHeartbeatKeepsALongStepAudible(t *testing.T) {
	beats := make(chan time.Duration, 8)
	stop := heartbeat(2*time.Millisecond, func(d time.Duration) { beats <- d })
	for i := 0; i < 3; i++ {
		select {
		case d := <-beats:
			if d <= 0 {
				t.Fatalf("beat %d reported %v elapsed", i, d)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("silence: beat %d never came", i)
		}
	}
	stop()
	// stop waits for the ticker to quit, so nothing is left beating behind a
	// finished conversion.
	drain(beats)
	select {
	case d := <-beats:
		t.Fatalf("a beat after stop: %v", d)
	case <-time.After(20 * time.Millisecond):
	}
}

func drain(c chan time.Duration) {
	for {
		select {
		case <-c:
		default:
			return
		}
	}
}
