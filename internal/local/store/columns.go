package store

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/josephburnett/gridwell/api/rpc"
)

// A tiles or grids column is described ONCE — here. Everything that used to
// spell a column list out again is derived from this table: the CREATE TABLE
// text (schema.go), the SELECT list and the Scan argument order (grids.go),
// the clone INSERT (clone.go), and every rebuild migration's copy list
// (migrations.go). Adding a column is one entry here plus a migration.
//
// That is the fix for a class, not an instance. The lists were five separate
// spellings of one fact, held together by two drift lints — and the class
// still bit: insertTileCopy's hand-listed INSERT had silently omitted
// content_zoom, url_history and alt_user, so clones lost a user's content
// zoom, their back-stack, and the "this name is the user's" latch. A list
// that cannot be spelled twice cannot be spelled inconsistently.
//
// (The per-kind CREATE statements — insert a well, a url, an external's row —
// are NOT in this class. Each names only the columns that kind sets and
// leaves the rest at their DDL defaults, so a new column does not touch
// them.)
type column[T any] struct {
	// name is the SQL column name. For a column that is on the wire it is
	// also the proto field name — TestDescriptorMatchesProto pins that.
	name string
	// ddl is the SQL that follows the name in CREATE TABLE: type, NOT NULL,
	// DEFAULT, REFERENCES, CHECK.
	ddl string
	// comment is the DDL comment written above the column. It is the
	// column's documentation, so it lives with the column and not in a
	// separate block of prose that can go stale.
	comment string
	// since is the schema version whose DATA this column carries, which is
	// what a rebuild migration needs to know: a rebuild copies every column
	// the version it READS already had. Usually the version that added the
	// column; for view_cx/view_cy it is 1, because the data existed at v1 as
	// view_x/view_y and rebuildSelect converts it in flight.
	since int
	// bind, when non-nil, makes this column part of the RECORD on the wire:
	// it returns the destination a row scan reads this column into. nil is
	// storage-only — a fact the store keeps for itself.
	bind func(*T) any
	// noCopy, when non-empty, is the reason a clone deliberately does NOT
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
		comment: `well: the framing this doorway was left at — a float CENTER in the
child grid's coordinates plus the pane-size-independent intrinsic
zoom (live / overtake). One shape, shared with a root grid's
root_cx/cy/zoom. view_zoom = 0 is the one "never visited"
convention: cx/cy carry no meaning then and the reader falls back
to the preview calibration. (view_x/view_y — an integer window
ORIGIN — retired at v11, docs/simplify-plan.md S4; the data is the
same data, which is why since is 1.)`,
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
		comment: `Canonical display label. Stamped at insert time. The client renders
alt_text verbatim (no derivation). Empty string until something stamps
it (e.g. a URL tile before its page title is captured).`,
		bind: func(t *rpc.Tile) any { return &t.AltText },
	},
	{
		name: "alt_user", ddl: "INTEGER NOT NULL DEFAULT 0", since: 2,
		comment: `alt_user=1 marks alt_text as USER-OWNED (the rename gesture, issue
#61): automatic captures (a url's page title on freeze, a shell's
foreground command on detach) must not overwrite a name the user set.
Storage-only — the latch is the store's rule, never a client's to set.
Added post-v1 (schema v2, additive).`,
	},
	{
		name: "content_zoom", ddl: "REAL NOT NULL DEFAULT 0", since: 3,
		comment: `content_zoom scales the content rendered INSIDE a text/shell/url tile
(the text font, the terminal font, the page zoom — issue #82).
Framing, never bumps version; 0 = unset (renders at 1.0). Added
post-v1 (schema v3, additive).`,
		bind: func(t *rpc.Tile) any { return &t.ContentZoom },
	},
	{
		name: "url_history", ddl: "TEXT", since: 4,
		comment: `url_history is a url tile's persisted navigation back-stack (JSON
{index, entries:[{url,title}]}, capped) captured at freeze so a
revived tile can still go "back" (issue #113). Content, rides the
versioned freeze writeback. Added post-v1 (schema v4, additive).`,
		bind: func(t *rpc.Tile) any { return nullString{&t.URLHistory} },
	},
	{
		name: "link_target_id", ddl: "TEXT", since: 6,
		comment: `link_target_id makes a LEAF tile (text/url/shell/pane) a LINK: a
qualified "<uuid>/<tile-id>" reference to the tile that owns the
content. NULL = an ordinary owned tile. A link row stores no content
of its own (the CHECK's link branch enforces it) — readers resolve
bytes/preview/session through the target id. The well kind's link
variant remains a qualified child_grid_id (the exit well); this
column is never set on wells. Added post-v1 (schema v6, rebuild —
the CHECK gained the link branch).`,
		bind: func(t *rpc.Tile) any { return nullString{&t.LinkTargetID} },
	},
	{
		name: "url_frozen", ddl: "INTEGER NOT NULL DEFAULT 0", since: 7,
		comment: `url_frozen=1 is the USER'S standing freeze on a url tile (issue
#237): descending does not auto-go-live until the reconnect gesture
clears it. Framing, never bumps version. Added post-v1 (schema v7,
additive).`,
		bind: func(t *rpc.Tile) any { return intBool{&t.URLFrozen} },
	},
	{
		name: "ns", ddl: "TEXT NOT NULL DEFAULT ''", since: 9,
		comment: `ns/key/tombstoned: an EXTERNAL's row (docs/one-node.md §2.6). ns is
the owning plugin id ('' = home); key is the plugin's stable key
for the entry; tombstoned=1 retires the key forever (the id is
never reused; a recreated key mints fresh). Home rows carry the
defaults. Added post-v1 (schema v9, additive).`,
		noCopy: "a clone is home's (ns ''): externals' rows are never cloned, they are re-listed",
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
		comment: `ns names the grid's owner: '' = home; a plugin id = that plugin's
memory (docs/one-node.md §2.6 — one table for every namespace).
context_key is the plugin's stable key for the context this grid
projects ('' for home grids). Added post-v1 (schema v9, additive).`,
	},
	{name: "context_key", ddl: "TEXT NOT NULL DEFAULT ''", since: 9},
	{
		name: "root_cx", ddl: "REAL", since: 11,
		comment: `root_cx/cy/zoom: the framing of a ROOT grid — one with no doorway
tile to carry it — in exactly the shape a doorway's view_cx/cy/zoom
uses. NULL or zero zoom = never visited. Home's root is this row
with ns = '' (schema v11); a plugin context's is its own. It reaches
the client through the Info handshake's root_view_* fields, never as
a Grid field, which is why it is storage-only here.`,
	},
	{name: "root_cy", ddl: "REAL", since: 11},
	{name: "root_zoom", ddl: "REAL", since: 11},
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
// in the SAME order from the same table, so the two cannot disagree.
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
// column or names one that is not copied — so "add a column, forget the clone
// path" is a named error at the one place the copy happens, not a silently
// incomplete copy.
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
// data the schema version it READS already had. A rebuild always materializes
// the CURRENT tilesTableDDL, so columns added later simply take their DDL
// defaults, and columns since dropped are not carried at all — the
// convergence contract, derived instead of retyped per migration.
func rebuildColumns(reads int) string {
	var names []string
	for _, c := range tilesColumns {
		if c.since <= reads {
			names = append(names, c.name)
		}
	}
	return strings.Join(names, ", ")
}

// nullString / nullInt64 / intBool are the scan adapters for columns whose
// SQL shape is not the Go shape: a NULL reads as the Go zero value, and
// SQLite's 0/1 integer reads as a bool. One adapter per shape, named on the
// descriptor entry, so a nullable column cannot be scanned as non-nullable
// (which fails only on the first NULL row in production).
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
