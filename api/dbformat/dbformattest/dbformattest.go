// Package dbformattest holds test helpers for plugins adopting the dbformat
// contract — chiefly the schema-equivalence proof (template == frozen v1 +
// migration chain) that makes the fresh-DB stamp shortcut in
// dbformat.EnsureVersion sound. Mirrors the shellsvctest pattern: production
// code never imports this.
package dbformattest

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/api/dbformat"
	_ "modernc.org/sqlite"
)

// SchemaDump returns a canonical dump of a DB's schema (type/name/sql from
// sqlite_master, sorted; DDL normalized) for equality comparison. The
// normalization exists because ALTER TABLE ADD COLUMN rewrites the stored
// CREATE text in SQLite's own style (columns appended before the paren,
// "IF NOT EXISTS" dropped, comments gone) — a migrated table and a fresh
// template table are the same schema in different spelling. Column names,
// types, defaults, constraints, and CHECKs all still compare exactly.
func SchemaDump(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`SELECT type, name, COALESCE(sql,'') FROM sqlite_master ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var typ, name, ddl string
		if err := rows.Scan(&typ, &name, &ddl); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s %s\n%s\n---\n", typ, name, normalizeDDL(ddl))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// normalizeDDL strips SQL line comments, the IF NOT EXISTS clause, and
// collapses all whitespace to single spaces, so textual spelling never
// masks (or fakes) a real schema difference.
func normalizeDDL(ddl string) string {
	var lines []string
	for _, line := range strings.Split(ddl, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		lines = append(lines, line)
	}
	s := strings.Join(lines, " ")
	s = strings.ReplaceAll(s, "IF NOT EXISTS ", "")
	s = strings.Join(strings.Fields(s), " ")
	s = strings.ReplaceAll(s, " ,", ",")
	s = strings.ReplaceAll(s, "( ", "(")
	s = strings.ReplaceAll(s, " )", ")")
	return s
}

// AssertEquivalence proves template == v1 + chain: it applies the live
// template to one fresh DB, the frozen v1 plus the full migration chain to
// another, and fails if the resulting schemas differ. This is what keeps
// "add a column to the template but forget the migration" (or vice versa) a
// loud failure instead of a silently divergent old file.
func AssertEquivalence(t *testing.T, template, v1 string, chain []dbformat.Migration) {
	t.Helper()
	fresh := openMem(t)
	if _, err := fresh.Exec(template); err != nil {
		t.Fatalf("apply template: %v", err)
	}

	upgraded := openMem(t)
	if _, err := upgraded.Exec(v1); err != nil {
		t.Fatalf("apply frozen v1: %v", err)
	}
	ctx := context.Background()
	tx, err := upgraded.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range chain {
		if err := m.Run(ctx, tx); err != nil {
			t.Fatalf("migration to v%d: %v", m.To, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if got, want := SchemaDump(t, upgraded), SchemaDump(t, fresh); got != want {
		t.Errorf("v1 + chain diverges from the live template:\n-- v1+chain --\n%s\n-- template --\n%s", got, want)
	}
}

func openMem(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}
