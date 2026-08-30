package store

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/josephburnett/gridwell/api/gwerr"
	"sort"
	"strings"
)

// This file is the startup schema guard. migrateUp trusts user_version as the
// sole indicator of a DB's shape, but an unstamped file takes the fresh fast
// path and is stamped at the current version without its columns ever being
// checked: tablesDDL()'s CREATE TABLE IF NOT EXISTS no-ops on the
// already-present tables and leaves their old shape intact. A column the
// current writer no longer populates then lurks until the first insert fails
// its constraint, which presents to the user as a tile that disappeared.
//
// verifySchema closes that gap: after migrations, it compares the DB's actual
// columns against the shape this binary materializes and fails loudly, naming
// the divergent column, instead of stamping and proceeding. The column
// fingerprint it uses is the same one the migration equivalence tests
// compare.

// ErrSchemaDivergence reports that an opened database's table shape does not
// match the schema this binary materializes: an out-of-contract DB that must
// be recreated, not silently used.
var ErrSchemaDivergence = gwerr.ErrSchemaDivergence

// colFP is a column's identity for equivalence: the column name is the map
// key, so only type, notnull, default, and pk distinguish two columns of the
// same name. cid is deliberately excluded — ALTER TABLE ADD COLUMN always
// appends, giving a higher cid, while inline DDL places columns mid-table, so
// a migrated schema and a fresh one are equivalent with different column
// orders. Shared by verifySchema and the migration equivalence tests.
type colFP struct {
	typ     string
	notNull bool
	dflt    string
	pk      int
}

// normalizeDefault canonicalizes a column default so an inline default and the
// identical ADD COLUMN default compare equal: NULL becomes "", spaces are
// trimmed, and one layer of surrounding matching quotes is stripped.
func normalizeDefault(d sql.NullString) string {
	if !d.Valid {
		return ""
	}
	v := strings.TrimSpace(d.String)
	if len(v) >= 2 {
		if (v[0] == '\'' && v[len(v)-1] == '\'') || (v[0] == '"' && v[len(v)-1] == '"') {
			v = v[1 : len(v)-1]
		}
	}
	return v
}

// tableColumnFPs reads each column of `table` as a colFP via PRAGMA table_info.
// A non-existent table yields an empty map (PRAGMA returns no rows, no error).
func tableColumnFPs(ctx context.Context, q gridReader, table string) (map[string]colFP, error) {
	// PRAGMA cannot bind params; table names come from sqlite_master or the
	// canonical DDL, never user input.
	rows, err := q.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, fmt.Errorf("table_info %s: %w", table, err)
	}
	defer rows.Close()
	out := map[string]colFP{}
	for rows.Next() {
		var (
			cid     int
			name    string
			typ     string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return nil, fmt.Errorf("scan table_info %s: %w", table, err)
		}
		out[name] = colFP{
			typ:     strings.ToUpper(strings.TrimSpace(typ)),
			notNull: notnull != 0,
			dflt:    normalizeDefault(dflt),
			pk:      pk,
		}
	}
	return out, rows.Err()
}

// userTableNames lists the non-internal tables of a database (skipping the
// sqlite_* bookkeeping tables), ordered by name.
func userTableNames(ctx context.Context, q gridReader) ([]string, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("scan table name: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// canonicalSchema is the column shape a fresh Open materializes: the current
// DDL applied to a throwaway in-memory DB. It is the one description of what
// every table's columns should be, read from the DDL and never hand-listed,
// so the guard cannot drift from the schema it protects.
func canonicalSchema(ctx context.Context) (map[string]map[string]colFP, error) {
	ref, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, fmt.Errorf("open reference db: %w", err)
	}
	defer ref.Close()
	ref.SetMaxOpenConns(1)
	for _, ddl := range []string{systemDDL, tablesDDL()} {
		if _, err := ref.ExecContext(ctx, ddl); err != nil {
			return nil, fmt.Errorf("apply reference ddl: %w", err)
		}
	}
	tables, err := userTableNames(ctx, ref)
	if err != nil {
		return nil, err
	}
	out := make(map[string]map[string]colFP, len(tables))
	for _, t := range tables {
		cols, err := tableColumnFPs(ctx, ref, t)
		if err != nil {
			return nil, err
		}
		out[t] = cols
	}
	return out, nil
}

// verifySchema fails with ErrSchemaDivergence when the open DB's columns do
// not match canonicalSchema: a missing column, a differing column by type,
// notnull, default, or pk, or an extra column the current writer never
// populates. The error lists every divergence so the cause is named rather
// than guessed. Extra non-canonical tables are ignored, since no writes target
// them; the guard is about the columns inserts and reads actually touch.
func (s *Store) verifySchema(ctx context.Context) error {
	want, err := canonicalSchema(ctx)
	if err != nil {
		return fmt.Errorf("build reference schema: %w", err)
	}
	var diffs []string
	for table, wantCols := range want {
		gotCols, err := tableColumnFPs(ctx, s.db, table)
		if err != nil {
			return err
		}
		if len(gotCols) == 0 {
			diffs = append(diffs, fmt.Sprintf("table %q is missing", table))
			continue
		}
		for col, wc := range wantCols {
			gc, ok := gotCols[col]
			if !ok {
				diffs = append(diffs, fmt.Sprintf("%s.%s is missing", table, col))
				continue
			}
			if wc != gc {
				diffs = append(diffs, fmt.Sprintf("%s.%s differs (want %+v, got %+v)", table, col, wc, gc))
			}
		}
		for col := range gotCols {
			if _, ok := wantCols[col]; !ok {
				diffs = append(diffs, fmt.Sprintf("%s.%s is not in this binary's schema", table, col))
			}
		}
	}
	if len(diffs) > 0 {
		sort.Strings(diffs)
		return fmt.Errorf("%w: %s", ErrSchemaDivergence, strings.Join(diffs, "; "))
	}
	return nil
}
