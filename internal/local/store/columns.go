package store

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/josephburnett/gridwell/api/rpc"
)

// A tiles or grids column is described once, here. Everything that would
// otherwise spell a column list out again is derived from this table: the
// CREATE TABLE text (schema.go), the SELECT list and the Scan argument order
// (grids.go), the clone INSERT (clone.go), and every rebuild migration's copy
// list (migrations.go). Adding a column is one entry here plus a migration. A
// list that cannot be spelled twice cannot be spelled inconsistently.
//
// The per-kind CREATE statements — insert a well, a url, a plugin-memory row
// — are not in this class. Each names only the columns that kind sets and
// leaves the rest at their DDL defaults, so a new column does not touch
// them.
type column[T any] struct {
	// name is the SQL column name. For a column that is on the wire it is
	// also the proto field name; TestDescriptorMatchesProto pins that.
	name string
	// ddl is the SQL that follows the name in CREATE TABLE: type, NOT NULL,
	// DEFAULT, REFERENCES, CHECK.
	ddl string
	// comment is the DDL comment written above the column. It is the
	// column's documentation, so it lives with the column and not in a
	// separate block of prose that can go stale.
	comment string
	// since is the schema version whose data this column carries, which is
	// what a rebuild migration needs to know: a rebuild copies every column
	// the version it reads already had. It is usually the version that added
	// the column; for view_cx and view_cy it is 1, because the data existed
	// at v1 as view_x and view_y and rebuildSelect converts it in flight.
	since int
	// bind, when non-nil, makes this column part of the record on the wire:
	// it returns the destination a row scan reads this column into. nil is
	// storage-only, a fact the store keeps for itself.
	bind func(*T) any
	// noCopy, when non-empty, is the reason a clone deliberately does not
	// copy this column. Every column is one or the other, and
	// TestEveryTileColumnIsCopiedOrExcused holds that line.
	noCopy string
}

// tilesColumns is the tiles table, in DDL order.
var tilesColumns = []column[rpc.Tile]{
	{
		name: "id", ddl: "INTEGER PRIMARY KEY AUTOINCREMENT", since: 1,
		comment: `AUTOINCREMENT for the same reason as grids: a reused tile id would
collide with the client's per-tile caches (e.g. the URL preview cache
keyed by tile id), showing a deleted tile's frozen frame on a new one.`,
		bind:   func(t *rpc.Tile) any { return &t.ID },
		noCopy: "the copy's identity — freshly assigned, never reused",
	},
	{
		name: "version", ddl: "INTEGER NOT NULL DEFAULT 0", since: 1,
		bind: func(t *rpc.Tile) any { return &t.Version },
	},
	{
		name: "grid_id", ddl: "INTEGER NOT NULL REFERENCES grids(id)", since: 1,
		bind: func(t *rpc.Tile) any { return &t.GridID },
	},
	{
		name: "kind", ddl: "TEXT NOT NULL CHECK (kind IN ('well','text','url','shell','pane'))", since: 1,
		bind: func(t *rpc.Tile) any { return &t.Kind },
	},
	{name: "x", ddl: "INTEGER NOT NULL", since: 1, bind: func(t *rpc.Tile) any { return &t.X }},
	{name: "y", ddl: "INTEGER NOT NULL", since: 1, bind: func(t *rpc.Tile) any { return &t.Y }},
	{name: "w", ddl: "INTEGER NOT NULL DEFAULT 1 CHECK (w > 0)", since: 1, bind: func(t *rpc.Tile) any { return &t.W }},
	{name: "h", ddl: "INTEGER NOT NULL DEFAULT 1 CHECK (h > 0)", since: 1, bind: func(t *rpc.Tile) any { return &t.H }},
	{
		name: "view_cx", ddl: "REAL NOT NULL DEFAULT 0", since: 1,
		comment: `well: the framing this doorway was left at — a float center in the
child grid's coordinates plus the pane-size-independent intrinsic
zoom (live over overtake). One shape, shared with a root grid's
root_cx/cy/zoom. view_zoom = 0 is the one "never visited"
convention: cx/cy carry no meaning then and the reader falls back
to the preview calibration. since is 1 because the data existed at
v1 as the integer origin view_x/view_y, which v11 converted.`,
		bind: func(t *rpc.Tile) any { return &t.ViewCx },
	},
	{name: "view_cy", ddl: "REAL NOT NULL DEFAULT 0", since: 1, bind: func(t *rpc.Tile) any { return &t.ViewCy }},
	{name: "view_zoom", ddl: "REAL NOT NULL DEFAULT 0", since: 1, bind: func(t *rpc.Tile) any { return &t.ViewZoom }},
	{
		name: "child_grid_id", ddl: "INTEGER", since: 1,
		comment: `No FK on child_grid_id: an exit well's child grid lives in another
plugin (a qualified "<uuid>/<id>" reference), so the link is a soft
pointer. Interior well integrity rests on the refcount machinery +
property test.`,
		bind: func(t *rpc.Tile) any { return nullString{&t.ChildGridID} },
	},
	{
		name: "text_x", ddl: "INTEGER NOT NULL DEFAULT 0", since: 1,
		comment: `text-only: the framed window in doc-space px (scroll offset + size)
plus rendered/text mode.`,
		bind: func(t *rpc.Tile) any { return &t.TextX },
	},
	{name: "text_y", ddl: "INTEGER NOT NULL DEFAULT 0", since: 1, bind: func(t *rpc.Tile) any { return &t.TextY }},
	{name: "text_w", ddl: "INTEGER NOT NULL DEFAULT 0", since: 1, bind: func(t *rpc.Tile) any { return &t.TextW }},
	{name: "text_h", ddl: "INTEGER NOT NULL DEFAULT 0", since: 1, bind: func(t *rpc.Tile) any { return &t.TextH }},
	{name: "text_mode", ddl: "TEXT", since: 1, bind: func(t *rpc.Tile) any { return nullString{&t.TextMode} }},
	{name: "blob_id", ddl: "INTEGER REFERENCES blobs(id)", since: 1, bind: func(t *rpc.Tile) any { return nullInt64{&t.BlobID} }},
	{
		name: "url_string", ddl: "TEXT", since: 1,
		comment: `url-only: the URL string. The frozen JPEG preview from last close
lives in the blobs table; preview_blob_id points at it (NULL until
first close). Hash-deduped across clones the same way text content
is — clones that haven't navigated independently share one row.`,
		bind: func(t *rpc.Tile) any { return nullString{&t.URLString} },
	},
	{
		name: "preview_blob_id", ddl: "INTEGER REFERENCES blobs(id)", since: 1,
		bind: func(t *rpc.Tile) any { return nullInt64{&t.PreviewBlobID} },
	},
	{
		name: "alt_text", ddl: "TEXT NOT NULL DEFAULT ''", since: 1,
		comment: `The canonical display label, stamped at insert time. The client
renders alt_text verbatim, with no derivation. It is the empty string
until something stamps it, as on a url tile before its page title is
captured.`,
		bind: func(t *rpc.Tile) any { return &t.AltText },
	},
	{
		name: "alt_user", ddl: "INTEGER NOT NULL DEFAULT 0", since: 2,
		comment: `alt_user=1 marks alt_text as user-owned, set by the rename gesture:
automatic captures (a url's page title on freeze, a shell's foreground
command on detach) must not overwrite a name the user set.
Storage-only — the latch is the store's rule, never a client's to set.
Added at schema v2, additive.`,
	},
	{
		name: "content_zoom", ddl: "REAL NOT NULL DEFAULT 0", since: 3,
		comment: `content_zoom scales the content rendered inside a text, shell, or url
tile: the text font, the terminal font, the page zoom. It is framing
and never bumps version; 0 is unset and renders at 1.0. Added at
schema v3, additive.`,
		bind: func(t *rpc.Tile) any { return &t.ContentZoom },
	},
	{
		name: "url_history", ddl: "TEXT", since: 4,
		comment: `url_history is a url tile's persisted navigation back-stack — JSON
{index, entries:[{url,title}]}, capped — captured at freeze so a
revived tile can still go back. Content; it rides the freeze
writeback. Added at schema v4, additive.`,
		bind: func(t *rpc.Tile) any { return nullString{&t.URLHistory} },
	},
	{
		name: "link_target_id", ddl: "TEXT", since: 6,
		comment: `link_target_id makes a leaf tile (text, url, shell, pane) a link: a
qualified "<uuid>/<tile-id>" reference to the tile that owns the
content. NULL is an ordinary owned tile. A link row stores no content
of its own, which the CHECK's link branch enforces, and readers
resolve bytes, preview, and session through the target id. The well
kind's link variant is a qualified child_grid_id, the exit well, so
this column is never set on wells. Added at schema v6, by rebuild:
the CHECK gained the link branch.`,
		bind: func(t *rpc.Tile) any { return nullString{&t.LinkTargetID} },
	},
	{
		name: "url_frozen", ddl: "INTEGER NOT NULL DEFAULT 0", since: 7,
		comment: `url_frozen=1 is the user's standing freeze on a url tile: descending
does not auto-go-live until the reconnect gesture clears it. Framing;
it never bumps version. Added at schema v7, additive.`,
		bind: func(t *rpc.Tile) any { return intBool{&t.URLFrozen} },
	},
	{
		name: "ns", ddl: "TEXT NOT NULL DEFAULT ''", since: 9,
		comment: `ns/key/tombstoned describe a plugin-memory row. ns is the owning
plugin id ('' is home); key is the plugin's stable key for the entry;
tombstoned=1 retires the key forever, so the id is never reused and a
recreated key mints fresh. Home rows carry the defaults. Added at
schema v9, additive.`,
		noCopy: "a clone lands in home (ns ''): plugin rows are re-listed, never cloned",
	},
	{
		name: "key", ddl: "TEXT NOT NULL DEFAULT ''", since: 9,
		noCopy: "an external's key — '' on every home row",
	},
	{
		name: "tombstoned", ddl: "INTEGER NOT NULL DEFAULT 0", since: 9,
		noCopy: "an external's retirement — 0 on every home row",
	},
	{name: "created_at", ddl: "INTEGER NOT NULL", since: 1},
	{name: "updated_at", ddl: "INTEGER NOT NULL", since: 1},
}

// gridsColumns is the grids table, in DDL order.
var gridsColumns = []column[rpc.Grid]{
	{
		name: "id", ddl: "INTEGER PRIMARY KEY AUTOINCREMENT", since: 1,
		comment: `AUTOINCREMENT so a deleted grid's id is never reused. Without it,
SQLite recycles the rowid of a deleted grid (e.g. an interior well that
was deleted, taking its owned child grid with it), and a new grid taking
that id would collide with the client's still-cached copy of the old
grid — making a fresh well render the deleted well's tiles. Fresh ids
keep the client cache (keyed by id) honest.

No refcount: grids are owned 1:1 by their parent well (copy-on-clone
never shares a grid), so only blobs are reference-counted.`,
		bind: func(g *rpc.Grid) any { return &g.ID },
	},
	{name: "version", ddl: "INTEGER NOT NULL DEFAULT 0", since: 1, bind: func(g *rpc.Grid) any { return &g.Version }},
	{name: "created_at", ddl: "INTEGER NOT NULL", since: 1},
	{name: "updated_at", ddl: "INTEGER NOT NULL DEFAULT 0", since: 1},
	{
		name: "ns", ddl: "TEXT NOT NULL DEFAULT ''", since: 9,
		comment: `ns names the grid's owner: '' is home, a plugin id is that plugin's
memory. One table serves every namespace. context_key is the plugin's
stable key for the context this grid projects, and is '' for home
grids. Added at schema v9, additive.`,
	},
	{name: "context_key", ddl: "TEXT NOT NULL DEFAULT ''", since: 9},
	{
		name: "root_cx", ddl: "REAL", since: 11,
		comment: `root_cx/cy/zoom is the framing of a root grid — one with no doorway
tile to carry it — in exactly the shape a doorway's view_cx/cy/zoom
uses. A NULL or zero zoom means never visited. Home's root is this
row with ns = ''; a plugin context's is its own. It reaches the client
through the Info handshake's root_view_* fields, never as a Grid
field, which is why it is storage-only here.`,
	},
	{name: "root_cy", ddl: "REAL", since: 11},
	{name: "root_zoom", ddl: "REAL", since: 11},
}

// connectionsColumns is the connections table, in DDL order: what the node
// remembers about a connection beyond what server.yaml declares. It is
// storage-only — no column is on the wire, and internal/connection owns the
// queries that read and write these rows — but the SHAPE is the store's, so
// the table evolves through the one migration chain like every other node
// fact. It reached the chain at v13, which adopted the table
// internal/connection used to create for itself; the rows in a live home are
// older than that, and v13 takes them exactly as they stand.
var connectionsColumns = []column[struct{}]{
	{
		name: "name", ddl: "TEXT PRIMARY KEY", since: 13,
		comment: `The connection's immutable name, as server.yaml declares it. It is
the namespace segment stored references are written through, so it is
never reassigned and never held twice.`,
	},
	{
		name: "remote_root", ddl: "TEXT NOT NULL DEFAULT ''", since: 13,
		comment: `The learned landing: the far node's home grid id, remembered so a
dark remote still has a room to show through the source cache. '' is
a connection that has never answered.`,
	},
	{
		name: "deleted", ddl: "INTEGER NOT NULL DEFAULT 0", since: 13,
		comment: `The retirement mirror, and not a fact of its own: retired_names in
server.yaml is the one owner of retirement, reconciled onto this row
at boot so route and Probe can read it off the row they already hold.
A name the config merely stopped declaring is NOT retired and keeps
its 0.`,
	},
}

// createTable renders a CREATE TABLE from a column table: each column's
// comment, then its name padded to the table's widest, then its DDL.
// trailing is the table-level constraint text (the tiles CHECK), appended
// after the columns.
func createTable[T any](name string, cols []column[T], trailing string) string {
	width := 0
	for _, c := range cols {
		if len(c.name) > width {
			width = len(c.name)
		}
	}
	var b strings.Builder
	b.WriteString("\nCREATE TABLE IF NOT EXISTS " + name + " (\n")
	for i, c := range cols {
		if c.comment != "" {
			for _, line := range strings.Split(c.comment, "\n") {
				if line == "" {
					b.WriteString("    --\n")
					continue
				}
				b.WriteString("    -- " + line + "\n")
			}
		}
		b.WriteString("    " + c.name + strings.Repeat(" ", width-len(c.name)) + " " + c.ddl)
		if i < len(cols)-1 || trailing != "" {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString(trailing)
	b.WriteString(");\n")
	return b.String()
}

// wireColumns is the SELECT list for reading a record: every column that is
// part of the row on the wire, in DDL order. scanDests produces destinations
// in the same order from the same table, so the two cannot disagree.
func wireColumns[T any](cols []column[T]) string {
	var names []string
	for _, c := range cols {
		if c.bind != nil {
			names = append(names, c.name)
		}
	}
	return strings.Join(names, ", ")
}

// scanDests returns the Scan destinations for wireColumns's list, bound to v.
func scanDests[T any](cols []column[T], v *T) []any {
	var out []any
	for _, c := range cols {
		if c.bind != nil {
			out = append(out, c.bind(v))
		}
	}
	return out
}

// copyColumns is the clone INSERT's column list: every tiles column except
// the ones a clone deliberately leaves behind (each with its reason on the
// descriptor entry).
func copyColumns() []string {
	var out []string
	for _, c := range tilesColumns {
		if c.noCopy == "" {
			out = append(out, c.name)
		}
	}
	return out
}

// copyBinding renders the clone INSERT's column list and its arguments from
// vals, in descriptor order. It refuses a vals map that is missing a copied
// column or names one that is not copied, so adding a column and forgetting
// the clone path is a named error at the one place the copy happens rather
// than a silently incomplete copy.
func copyBinding(vals map[string]any) (cols string, args []any, err error) {
	names := copyColumns()
	args = make([]any, 0, len(names))
	for _, n := range names {
		v, ok := vals[n]
		if !ok {
			return "", nil, fmt.Errorf("tile copy: no value for column %q "+
				"(add it to insertTileCopy, or give the column a noCopy reason)", n)
		}
		args = append(args, v)
	}
	if len(vals) != len(names) {
		known := map[string]bool{}
		for _, n := range names {
			known[n] = true
		}
		for n := range vals {
			if !known[n] {
				return "", nil, fmt.Errorf("tile copy: value for %q, which is not a copied column", n)
			}
		}
	}
	return strings.Join(names, ", "), args, nil
}

// rebuildColumns is a rebuild migration's copy list: every tiles column whose
// data the schema version it reads already had. A rebuild always materializes
// the current tilesTableDDL, so columns added later take their DDL defaults
// and columns since dropped are not carried at all. That is the convergence
// contract, derived rather than retyped per migration.
func rebuildColumns(reads int) string {
	var names []string
	for _, c := range tilesColumns {
		if c.since <= reads {
			names = append(names, c.name)
		}
	}
	return strings.Join(names, ", ")
}

// nullString, nullInt64, and intBool are the scan adapters for columns whose
// SQL shape is not the Go shape: a NULL reads as the Go zero value, and
// SQLite's 0 or 1 integer reads as a bool. One adapter per shape, named on
// the descriptor entry, so a nullable column cannot be scanned as
// non-nullable, which would fail only on the first NULL row in
// production.
type nullString struct{ p *string }

func (n nullString) Scan(v any) error {
	var ns sql.NullString
	if err := ns.Scan(v); err != nil {
		return err
	}
	*n.p = ns.String
	return nil
}

type nullInt64 struct{ p *int64 }

func (n nullInt64) Scan(v any) error {
	var ni sql.NullInt64
	if err := ni.Scan(v); err != nil {
		return err
	}
	*n.p = ni.Int64
	return nil
}

type intBool struct{ p *bool }

func (n intBool) Scan(v any) error {
	var ni sql.NullInt64
	if err := ni.Scan(v); err != nil {
		return err
	}
	*n.p = ni.Int64 != 0
	return nil
}
