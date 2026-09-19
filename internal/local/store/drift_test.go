package store

import (
	"sort"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// The two remaining descriptions of a record are bound to each other: the
// proto, which owns what a Tile is, and the store's column descriptor, which
// owns how a row is stored. The claim is about the on-wire set only and is
// exact both ways: a column marked on the wire is a proto field of the same
// name, and every proto field is either such a column or on the wireOnly list
// with a written reason.
func TestDescriptorMatchesProto(t *testing.T) {
	cases := []struct {
		table    string
		message  protoreflect.MessageDescriptor
		onWire   []string
		wireOnly map[string]string
	}{
		{
			table:   "grids",
			message: (&pb.Grid{}).ProtoReflect().Descriptor(),
			onWire:  wireNames(gridsColumns),
			wireOnly: map[string]string{
				"writable":        "stamped by the serving node from the owning plugin's Info — a per-grid capability, never persisted",
				"scratch_grid_id": "stamped by the serving node, qualified per hop",
				"node_ns":         "the namespace chain of the node serving the grid, from the receiver's perspective",
				"menu_entries":    "stamped by the serving node from the owning plugin's Info",
				"host_content":    "declared by the owning plugin (these rows project host state); stamped by the adapter that serves the grid",
				"glyph":           "likewise — the owning plugin's declared identity face for the grid",
			},
		},
		{
			table:   "tiles",
			message: (&pb.Tile{}).ProtoReflect().Descriptor(),
			onWire:  wireNames(tilesColumns),
			wireOnly: map[string]string{
				"reference":         `derived by the router from a qualified child_grid_id — the one authoritative "is a link" signal`,
				"serves_page":       "declared by the owning plugin from its own content (fs: the filename's media type)",
				"text_presentation": "likewise — how the owning plugin says a text body presents",
				"status_detail":     "the owning plugin's current trouble with this tile (an ssh well's last dial error)",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.table, func(t *testing.T) {
			cols := stringSet(c.onWire)
			fields := protoFieldNames(c.message)

			var notFields, notColumns []string
			for col := range cols {
				if _, ok := fields[col]; !ok {
					notFields = append(notFields, col)
				}
			}
			for f := range fields {
				if _, ok := cols[f]; ok {
					continue
				}
				if _, ok := c.wireOnly[f]; ok {
					continue
				}
				notColumns = append(notColumns, f)
			}
			sort.Strings(notFields)
			sort.Strings(notColumns)
			if len(notFields) > 0 {
				t.Errorf("%s columns marked on-wire with no proto field of that name: %v", c.table, notFields)
			}
			if len(notColumns) > 0 {
				t.Errorf("proto %s fields that are neither a stored column nor on the wire-only list: %v "+
					"(store it, or add it to wireOnly with the reason it is derived)", c.message.Name(), notColumns)
			}
			// A wire-only entry is a claim; a stale one hides the next real
			// finding under it.
			for f := range c.wireOnly {
				if _, ok := fields[f]; !ok {
					t.Errorf("wireOnly names %q, which %s no longer declares", f, c.message.Name())
				}
				if _, ok := cols[f]; ok {
					t.Errorf("wireOnly names %q, which the descriptor now stores", f)
				}
			}
		})
	}
}

// wireNames is the set of column names a table puts on the wire.
func wireNames[T any](cols []column[T]) []string {
	var out []string
	for _, c := range cols {
		if c.bind != nil {
			out = append(out, c.name)
		}
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
