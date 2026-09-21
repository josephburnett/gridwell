package store

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/josephburnett/gridwell/api/gwerr"
	"sort"
	"strings"
)

// The startup schema guard. migrateUp trusts user_version alone, but an
// unstamped file takes the fresh fast path and is stamped without its columns
// being checked, because CREATE TABLE IF NOT EXISTS no-ops on an
// already-present table and leaves its old shape intact. The stale shape then
// lurks until an insert fails its constraint, which the user sees as a tile
// that disappeared. verifySchema compares the DB's actual columns against the
// shape this binary materializes and fails loudly instead.

// ErrSchemaDivergence reports a database whose table shape does not match the
// schema this binary materializes.
var ErrSchemaDivergence = gwerr.ErrSchemaDivergence

// colFP is a column's identity for equivalence. cid is deliberately excluded:
// ADD COLUMN appends while inline DDL places columns mid-table, so a migrated
// and a fresh schema are equivalent with different column orders.
type colFP struct {
	typ     string
	notNull bool
	dflt    string
	pk      int
}

// normalizeDefault makes an inline default and the identical ADD COLUMN
// default compare equal.
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

// tableColumnFPs reads each column of `table` via PRAGMA table_info. A
// non-existent table yields an empty map.
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

// userTableNames lists a database's tables, skipping sqlite_* bookkeeping.
func userTableNames(ctx context.Context, q gridReader) ([]string, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	return collect(rows, func(rows *sql.Rows) (string, error) {
		var n string
		if err := rows.Scan(&n); err != nil {
			return "", fmt.Errorf("scan table name: %w", err)
		}
		return n, nil
	})
}

// canonicalSchema is the current DDL applied to a throwaway in-memory DB, read
// from the DDL and never hand-listed, so the guard cannot drift from the
// schema it protects.
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
// not match canonicalSchema, listing every divergence. Extra non-canonical
// tables are ignored: the guard is about the columns inserts and reads touch.
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
