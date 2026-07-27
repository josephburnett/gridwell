package store

import (
	"context"
	"sort"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// TestProtoMatchesDDL is the drift lint between the wire schema in
// data.proto and the hand-written SQLite schema in schema.go. Every
// proto field on a record message (Grid, Tile) must have a matching
// SQLite column with the same snake_case name; every column must
// either have a proto field or be on the documented storage-only
// allowlist. Run on every test invocation so a forgotten proto+DDL
// pair fails the build immediately.
func TestProtoMatchesDDL(t *testing.T) {
	s := newTestStore(t)
	cases := []struct {
		table       string
		message     protoreflect.MessageDescriptor
		storageOnly []string
		protoOnly   []string
	}{
		{
			table:   "grids",
			message: (&pb.Grid{}).ProtoReflect().Descriptor(),
			// source_kind/source_id are set by the fs/proc plugins on their
			// own GetGrid responses; the local store never persists them.
			// writable is stamped by the serving node from the owning
			// plugin's Info — wire-only, per-grid capability, never persisted.
			storageOnly: []string{"created_at", "updated_at"},
			protoOnly:   []string{"source_kind", "source_id", "writable", "scratch_grid_id", "proxy_endpoint", "create_schemas"},
		},
		{
			table:   "tiles",
			message: (&pb.Tile{}).ProtoReflect().Descriptor(),
			// alt_user is the server-side "user owns this name" latch (issue
			// #61) — consulted by the capture paths, never sent on the wire.
			storageOnly: []string{"created_at", "updated_at", "alt_user"},
			// reference is derived by the server (qualifyTiles) from a tile's
			// child_grid_id shape, never persisted — wire-only, so it has no
			// DDL column by design.
			protoOnly: []string{"reference"},
		},
	}
	for _, c := range cases {
		t.Run(c.table, func(t *testing.T) {
			cols := tableColumns(t, s, c.table)
			fields := protoFieldNames(c.message)
			storage := stringSet(c.storageOnly)
			protoOnly := stringSet(c.protoOnly)

			missingCols := []string{}
			for f := range fields {
				if _, ok := protoOnly[f]; ok {
					continue
				}
				if _, ok := cols[f]; !ok {
					missingCols = append(missingCols, f)
				}
			}
			missingFields := []string{}
			for col := range cols {
				if _, ok := storage[col]; ok {
					continue
				}
				if _, ok := fields[col]; ok {
					continue
				}
				missingFields = append(missingFields, col)
			}
			sort.Strings(missingCols)
			sort.Strings(missingFields)
			if len(missingCols) > 0 {
				t.Errorf("proto %s fields with no matching DDL column: %v",
					c.message.Name(), missingCols)
			}
			if len(missingFields) > 0 {
				t.Errorf("DDL %s columns with no matching proto field (add to proto or storage allowlist): %v",
					c.table, missingFields)
			}
		})
	}
}

// tableColumns reads column names from sqlite_master via PRAGMA.
func tableColumns(t *testing.T, s *Store, table string) map[string]struct{} {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(), "PRAGMA table_info("+table+")")
	if err != nil {
		t.Fatalf("table_info %s: %v", table, err)
	}
	defer rows.Close()
	out := map[string]struct{}{}
	for rows.Next() {
		var (
			cid       int
			name      string
			ctype     string
			notnull   int
			dfltValue any
			pk        int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		out[name] = struct{}{}
	}
	return out
}

// protoFieldNames returns the set of proto field names for a message
// (the snake_case names from the proto, not the Go field names).
func protoFieldNames(m protoreflect.MessageDescriptor) map[string]struct{} {
	out := map[string]struct{}{}
	fields := m.Fields()
	for i := 0; i < fields.Len(); i++ {
		out[string(fields.Get(i).Name())] = struct{}{}
	}
	return out
}

func stringSet(s []string) map[string]struct{} {
	out := make(map[string]struct{}, len(s))
	for _, v := range s {
		out[v] = struct{}{}
	}
	return out
}
