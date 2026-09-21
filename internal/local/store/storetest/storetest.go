// Package storetest renders a node database as text, so a test can say "this
// read changed nothing" about the file rather than about the row it asked for.
// A read that writes writes somewhere else.
package storetest

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// Snapshot is every row of every table, columns and all, in a stable order.
// Two snapshots differ exactly when a stored fact differs. sqlite_sequence is
// in it because a mint that is rolled back still spends an id, and a spent id
// is a fact when ids are never reused.
func Snapshot(t *testing.T, db *sql.DB) string {
	t.Helper()
	var tables []string
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatalf("storetest: list tables: %v", err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	for _, tbl := range tables {
		b.WriteString("== " + tbl + "\n")
		for _, line := range tableRows(t, db, tbl) {
			b.WriteString("  " + line + "\n")
		}
	}
	return b.String()
}

// Table is Snapshot for one table, for a test that wants to say which table
// the write landed in.
func Table(t *testing.T, db *sql.DB, table string) string {
	t.Helper()
	return strings.Join(tableRows(t, db, table), "\n")
}

func tableRows(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rs, err := db.Query(`SELECT * FROM "` + table + `"`)
	if err != nil {
		t.Fatalf("storetest: dump %s: %v", table, err)
	}
	defer rs.Close()
	cols, err := rs.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for rs.Next() {
		cells := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if err := rs.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		parts := make([]string, len(cols))
		for i, c := range cells {
			if raw, ok := c.([]byte); ok {
				parts[i] = fmt.Sprintf("%s=%x", cols[i], raw)
				continue
			}
			parts[i] = fmt.Sprintf("%s=%v", cols[i], c)
		}
		lines = append(lines, strings.Join(parts, " "))
	}
	if err := rs.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(lines)
	return lines
}

// Diff is the first line two snapshots disagree on: the row that was written.
func Diff(before, after string) string {
	b, a := strings.Split(before, "\n"), strings.Split(after, "\n")
	for i := 0; i < len(b) || i < len(a); i++ {
		var bl, al string
		if i < len(b) {
			bl = b[i]
		}
		if i < len(a) {
			al = a[i]
		}
		if bl != al {
			return "  before: " + bl + "\n   after: " + al
		}
	}
	return "  (rows equal; the difference is trailing)"
}

// Dump is the file as table → row id → column → value. The row id is the
// row's first column, which is the primary key of every table a node database
// has.
type Dump map[string]map[string]map[string]string

// DumpOf reads the whole file into a Dump.
func DumpOf(t *testing.T, db *sql.DB) Dump {
	t.Helper()
	out := Dump{}
	var tables []string
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`)
	if err != nil {
		t.Fatalf("storetest: list tables: %v", err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range tables {
		out[tbl] = map[string]map[string]string{}
		rs, err := db.Query(`SELECT * FROM "` + tbl + `"`)
		if err != nil {
			t.Fatalf("storetest: dump %s: %v", tbl, err)
		}
		cols, err := rs.Columns()
		if err != nil {
			t.Fatal(err)
		}
		for rs.Next() {
			cells := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range cells {
				ptrs[i] = &cells[i]
			}
			if err := rs.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			row := map[string]string{}
			for i, c := range cells {
				if raw, ok := c.([]byte); ok {
					row[cols[i]] = fmt.Sprintf("%x", raw)
					continue
				}
				row[cols[i]] = fmt.Sprintf("%v", c)
			}
			out[tbl][row[cols[0]]] = row
		}
		rs.Close()
		if err := rs.Err(); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// Changed names every difference between two dumps, sorted: "tiles[7].x" for a
// column that changed, "+tiles[9]" for a row that appeared, "-grids[3]" for one
// that went.
func Changed(before, after Dump) []string {
	var out []string
	for tbl, rows := range after {
		for id, row := range rows {
			old, ok := before[tbl][id]
			if !ok {
				out = append(out, "+"+tbl+"["+id+"]")
				continue
			}
			for col, v := range row {
				if old[col] != v {
					out = append(out, tbl+"["+id+"]."+col)
				}
			}
		}
	}
	for tbl, rows := range before {
		for id := range rows {
			if _, ok := after[tbl][id]; !ok {
				out = append(out, "-"+tbl+"["+id+"]")
			}
		}
	}
	sort.Strings(out)
	return out
}
