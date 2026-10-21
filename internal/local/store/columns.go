package store

import (
	"database/sql"
	"fmt"
	"strings"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// A tiles or grids column is described once, here. The CREATE TABLE text, the
// SELECT list and Scan order, the copy INSERT and every rebuild migration's
// copy list are derived from this table, so a column list that cannot be
// spelled twice cannot be spelled inconsistently. Adding a column is one entry
// here plus a migration. The per-kind CREATE statements are not in this class:
// each names only the columns that kind sets.
type column[T any] struct {
	// name is the SQL column name. For a column that is on the wire it is
	// also the proto field name unless field names one;
	// TestDescriptorMatchesProto pins that.
	name string
	// field is the proto field name of a column whose SQL name predates it,
	// since a column is never renamed (this package's CLAUDE.md).
	field string
	// ddl is the SQL that follows the name in CREATE TABLE: type, NOT NULL,
	// DEFAULT, REFERENCES, CHECK.
	ddl string
	// comment is the DDL comment written above the column, so the column's
	// documentation lives with the column.
	comment string
	// since is the schema version whose data this column carries: a rebuild
	// copies every column the version it reads already had. For view_cx and
	// view_cy it is 1, because the data existed at v1 as view_x and view_y
	// and rebuildSelect converts it in flight.
	since int
	// bind, when non-nil, makes this column part of the record on the wire:
	// it returns the destination a row scan reads this column into. nil is
	// storage-only, a fact the store keeps for itself.
	bind func(*T) any
	// noCopy, when non-empty, is the reason a copy deliberately does not
	// copy this column. Every column is one or the other, and
	// TestEveryTileColumnIsCopiedOrExcused holds that line.
	noCopy string
}

// tilesColumns is the tiles table, in DDL order.
var tilesColumns = []column[gridwellv1.Tile]{
	{
		name: "id", ddl: "INTEGER PRIMARY KEY AUTOINCREMENT", since: 1,
		comment: `AUTOINCREMENT for the same reason as grids: a reused tile id would
collide with the client's per-tile caches (e.g. the URL preview cache
keyed by tile id), showing a deleted tile's frozen frame on a new one.`,
		bind:   func(t *gridwellv1.Tile) any { return &t.Id },
		noCopy: "the copy's identity — freshly assigned, never reused",
	},
	{
		name: "version", ddl: "INTEGER NOT NULL DEFAULT 0", since: 1,
		bind: func(t *gridwellv1.Tile) any { return &t.Version },
	},
	{
		name: "grid_id", ddl: "INTEGER NOT NULL REFERENCES grids(id)", since: 1,
		bind: func(t *gridwellv1.Tile) any { return &t.GridId },
	},
	{
		name: "kind", ddl: "TEXT NOT NULL CHECK (kind IN ('door','text','url','shell','workspace'))", since: 1,
		bind: func(t *gridwellv1.Tile) any { return &t.Kind },
	},
	{name: "x", ddl: "INTEGER NOT NULL", since: 1, bind: func(t *gridwellv1.Tile) any { return &t.X }},
	{name: "y", ddl: "INTEGER NOT NULL", since: 1, bind: func(t *gridwellv1.Tile) any { return &t.Y }},
	{name: "w", ddl: "INTEGER NOT NULL DEFAULT 1 CHECK (w > 0)", since: 1, bind: func(t *gridwellv1.Tile) any { return &t.W }},
	{name: "h", ddl: "INTEGER NOT NULL DEFAULT 1 CHECK (h > 0)", since: 1, bind: func(t *gridwellv1.Tile) any { return &t.H }},
	{
		name: "view_cx", ddl: "REAL", since: 1,
		comment: `door: the framing this door was left at — a float center in the
child grid's coordinates plus the pane-size-independent intrinsic
zoom (live over overtake). One shape, shared with a root grid's
root_cx/cy/zoom: all three NULL when never visited, else an
rpc.Framing (v16). since is 1 because the data existed at v1 as the
integer origin view_x/view_y, which v11 converted.`,
		bind: func(t *gridwellv1.Tile) any { return nullFloat64{&t.ViewCx} },
	},
	{name: "view_cy", ddl: "REAL", since: 1, bind: func(t *gridwellv1.Tile) any { return nullFloat64{&t.ViewCy} }},
	{name: "view_zoom", ddl: "REAL", since: 1, bind: func(t *gridwellv1.Tile) any { return nullFloat64{&t.ViewZoom} }},
	{
		name: "child_grid_id", ddl: "INTEGER", since: 1,
		comment: `No FK on child_grid_id: the child grid of a door to another namespace
lives in another plugin (a qualified "<uuid>/<id>" link), so the link is a soft
pointer. Interior door integrity rests on the refcount machinery +
property test.`,
		bind: func(t *gridwellv1.Tile) any { return nullString{&t.ChildGridId} },
	},
	{
		name: "text_x", ddl: "INTEGER NOT NULL DEFAULT 0", since: 1,
		comment: `text-only: the scroll offset in doc-space px plus rendered/text mode.`,
		bind:    func(t *gridwellv1.Tile) any { return &t.TextX },
	},
	{name: "text_y", ddl: "INTEGER NOT NULL DEFAULT 0", since: 1, bind: func(t *gridwellv1.Tile) any { return &t.TextY }},
	{
		name: "text_w", ddl: "INTEGER NOT NULL DEFAULT 0", since: 1,
		comment: `text_w/text_h: dead storage, kept until a retiring rebuild. No client
writes them and nothing reads them, because the pane box a text tile is shown
in is the viewer's geometry, not a fact about the tile.`,
		bind: func(t *gridwellv1.Tile) any { return &t.TextW },
	},
	{name: "text_h", ddl: "INTEGER NOT NULL DEFAULT 0", since: 1, bind: func(t *gridwellv1.Tile) any { return &t.TextH }},
	{name: "text_mode", ddl: "TEXT", since: 1, bind: func(t *gridwellv1.Tile) any { return nullString{&t.TextMode} }},
	{name: "blob_id", ddl: "INTEGER REFERENCES blobs(id)", since: 1, bind: func(t *gridwellv1.Tile) any { return nullInt64{&t.BlobId} }},
	{
		name: "url_string", ddl: "TEXT", since: 1,
		comment: `url-only: the URL string. The frozen JPEG preview from last close
lives in the blobs table; preview_blob_id points at it (NULL until
first close). Hash-deduped across copies the same way text content
is — copies that haven't navigated independently share one row.`,
		bind: func(t *gridwellv1.Tile) any { return nullString{&t.UrlString} },
	},
	{
		name: "preview_blob_id", ddl: "INTEGER REFERENCES blobs(id)", since: 1,
		bind: func(t *gridwellv1.Tile) any { return nullInt64{&t.PreviewBlobId} },
	},
	{
		name: "alt_text", ddl: "TEXT NOT NULL DEFAULT ''", since: 1,
		comment: `The canonical display label, stamped at insert time. The client
renders alt_text verbatim, with no derivation. It is the empty string
until something stamps it, as on a url tile before its page title is
captured.`,
		bind: func(t *gridwellv1.Tile) any { return &t.AltText },
	},
	{
		name: "alt_user", ddl: "INTEGER NOT NULL DEFAULT 0", since: 2,
		comment: `alt_user=1 marks alt_text as user-owned, set by the rename gesture
and by a url born with a name (a copy across namespaces, insertURLRow):
automatic captures (a url's page title on freeze, a shell's foreground
command on detach) and a plugin's listing must not overwrite a name the
user set.
Storage-only — the latch is the store's rule, never a client's to set.
Added at schema v2, additive.`,
	},
	{
		name: "content_zoom", ddl: "REAL NOT NULL DEFAULT 0", since: 3,
		comment: `content_zoom scales the content rendered inside a text, shell, or url
tile: the text font, the terminal font, the page zoom. It is framing
and never bumps version; 0 is unset and renders at 1.0. Added at
schema v3, additive.`,
		bind: func(t *gridwellv1.Tile) any { return &t.ContentZoom },
	},
	{
		name: "url_history", ddl: "TEXT", since: 4,
		comment: `url_history is a url tile's persisted navigation back-stack — JSON
{index, entries:[{url,title}]}, capped — captured at freeze so a
revived tile can still go back. A capture: it rides the freeze
writeback and never bumps version. Added at schema v4, additive.`,
		bind: func(t *gridwellv1.Tile) any { return nullString{&t.UrlHistory} },
	},
	{
		name: "link_target_id", ddl: "TEXT", since: 6,
		comment: `link_target_id makes a leaf tile (text, url, shell, workspace) a link: a
qualified "<uuid>/<tile-id>" link to the tile that owns the
content, or on a plugin's row a bare entry address in that same plugin
(rpc.EntryTileID). NULL is an ordinary owned tile. A link row stores no content
of its own, which the CHECK's link branch enforces, and readers
resolve bytes, preview, and tmux through the target id. The door
kind's link variant is a qualified child_grid_id, the door to another namespace, so
this column is never set on doors. Added at schema v6, by rebuild:
the CHECK gained the link branch.`,
		bind: func(t *gridwellv1.Tile) any { return nullString{&t.LinkTargetId} },
	},
	{
		name: "url_frozen", ddl: "INTEGER NOT NULL DEFAULT 0", since: 7,
		comment: `url_frozen=1 is the user's standing freeze on a tile that can go
live, url or shell: descending does not auto-go-live until the
reconnect gesture clears it, and the preview is what the tile keeps
looking like. The name is from before a shell could be frozen.
Framing; it never bumps version. Added at schema v7, additive.`,
		bind: func(t *gridwellv1.Tile) any { return intBool{&t.UrlFrozen} },
	},
	{
		name: "ns", ddl: "TEXT NOT NULL DEFAULT ''", since: 9,
		comment: `ns/key/tombstoned describe a plugin's row. ns is the owning
plugin id ('' is home); key is the plugin's stable key for the entry;
tombstoned=1 retires the key forever, so the id is never reused and a
recreated key mints fresh. Home rows carry the defaults. Added at
schema v9, additive.`,
		noCopy: "a copy lands in home (ns ''): plugin rows are re-listed, never copied",
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
	{
		name: "serves_page", ddl: "INTEGER NOT NULL DEFAULT 0", since: 14,
		comment: `serves_page=1 is a plugin url row whose page the plugin serves, the
address derived at the /content/ API. Like kind and alt_text it is a
snapshot of the listing, so a row its source does not list right now
still presents as a page rather than as a url tile with no address.
Added at schema v14, additive.`,
		bind:   func(t *gridwellv1.Tile) any { return intBool{&t.ServesPage} },
		noCopy: "a copy lands in home, where nothing serves a page",
	},
	{
		name: "shell_session", field: "shell_tmux", ddl: "INTEGER", since: 15,
		comment: `shell_session is the id of the tile whose tmux a shell row attaches,
written on a copy so every copy shares its source's tmux. NULL is the
row's own id. It names a tmux, not a row: the tile that started it may
be gone while a copy still names it. On the wire it is shell_tmux.
Added at schema v15, additive.`,
		bind: func(t *gridwellv1.Tile) any { return nullString{&t.ShellTmux} },
	},
	{
		name: "content_at", ddl: "INTEGER", since: 19,
		comment: `content_at is when the row's content last changed, unix seconds,
stamped only where version bumps (bumpTileVersion). NULL is a row whose
content has not changed since created_at; contentAtSQL reads the two as
one time. Storage-only. Added at schema v19, additive.`,
		noCopy: "a copy's content is new when the copy is: NULL reads as its created_at",
	},
}

// gridsColumns is the grids table, in DDL order.
var gridsColumns = []column[gridwellv1.Grid]{
	{
		name: "id", ddl: "INTEGER PRIMARY KEY AUTOINCREMENT", since: 1,
		comment: `AUTOINCREMENT so a deleted grid's id is never reused. Without it,
SQLite recycles the rowid of a deleted grid (e.g. an interior door that
was deleted, taking its owned child grid with it), and a new grid taking
that id would collide with the client's still-cached copy of the old
grid — making a fresh door render the deleted door's tiles. Fresh ids
keep the client cache (keyed by id) honest.

No refcount: grids are owned 1:1 by their parent door (eager copy
never shares a grid), so only blobs are reference-counted.`,
		bind: func(g *gridwellv1.Grid) any { return &g.Id },
	},
	{name: "version", ddl: "INTEGER NOT NULL DEFAULT 0", since: 1, bind: func(g *gridwellv1.Grid) any { return &g.Version }},
	{name: "created_at", ddl: "INTEGER NOT NULL", since: 1},
	{name: "updated_at", ddl: "INTEGER NOT NULL DEFAULT 0", since: 1},
	{
		name: "ns", ddl: "TEXT NOT NULL DEFAULT ''", since: 9,
		comment: `ns names the grid's owner: '' is home, a plugin id is that plugin's
rows. One table serves every namespace. context_key is the plugin's
stable key for the grid this grid projects, and is '' for home
grids. Added at schema v9, additive.`,
	},
	{name: "context_key", ddl: "TEXT NOT NULL DEFAULT ''", since: 9},
	{
		name: "root_cx", ddl: "REAL", since: 11,
		comment: `root_cx/cy/zoom is the framing of a root grid — one with no door
tile to carry it — in exactly the shape a door's view_cx/cy/zoom
uses, NULL when never visited. Home's root is this
row with ns = ''; a plugin grid's is its own. It reaches the client
through the Info handshake's root_view_* fields, never as a Grid
field, which is why it is storage-only here.`,
	},
	{name: "root_cy", ddl: "REAL", since: 11},
	{name: "root_zoom", ddl: "REAL", since: 11},
	{
		name: "selection", ddl: "TEXT NOT NULL DEFAULT ''", since: 17,
		comment: `selection is the set of this grid's tiles the user has in hand, as
their row ids in ascending order joined by commas, so the same set is
always the same bytes. It names only live tiles on this grid (see
leaveSelection). Not content: writing it bumps no version.`,
		bind: func(g *gridwellv1.Grid) any { return selectionScan{&g.Selection} },
	},
}

// connectionsColumns is the connections table, in DDL order: what the node
// keeps about a connection beyond what server.yaml declares. No column is
// on the wire; the table evolves through the one migration chain.
var connectionsColumns = []column[struct{}]{
	{
		name: "name", ddl: "TEXT PRIMARY KEY", since: 13,
		comment: `The connection's immutable name, as server.yaml declares it. It is
the namespace segment stored links are written through, so it is
never reassigned and never held twice.`,
	},
	{
		name: "remote_root", ddl: "TEXT NOT NULL DEFAULT ''", since: 13,
		comment: `The learned landing: the far node's home grid id, kept so a
dark remote still has a grid to show through the source cache. '' is
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

// createTable renders a CREATE TABLE from a column table. trailing is the
// table-level constraint text, appended after the columns.
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

// wireColumns is the SELECT list for reading a record, in DDL order. scanDests
// produces destinations from the same table, so the two cannot disagree.
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

// copyColumns is every tiles column except the ones a copy deliberately
// leaves behind, each with its reason on the descriptor entry.
func copyColumns() []string {
	var out []string
	for _, c := range tilesColumns {
		if c.noCopy == "" {
			out = append(out, c.name)
		}
	}
	return out
}

// copyBinding renders the copy INSERT's column list and arguments from vals,
// in descriptor order. It refuses a map missing a copied column or naming an
// uncopied one, so a forgotten copy path is a named error rather than a
// silently incomplete copy.
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

// rebuildColumns is every tiles column whose data the schema version a rebuild
// reads already had. The rebuild materializes the current tilesTableDDL, so
// later columns take their DDL defaults and dropped ones are not carried. That
// is the convergence contract, derived rather than retyped per migration.
func rebuildColumns(reads int) string {
	var names []string
	for _, c := range tilesColumns {
		if c.since <= reads {
			names = append(names, c.name)
		}
	}
	return strings.Join(names, ", ")
}

// nullString, nullInt64, nullFloat64 and intBool are the scan adapters for columns whose
// SQL shape is not the Go shape. One per shape, named on the descriptor entry,
// so a nullable column cannot be scanned as non-nullable, which would fail
// only on the first NULL row in production.
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

// nullFloat64 reads NULL as 0, which on the wire is the absent field.
type nullFloat64 struct{ p *float64 }

func (n nullFloat64) Scan(v any) error {
	var nf sql.NullFloat64
	if err := nf.Scan(v); err != nil {
		return err
	}
	*n.p = nf.Float64
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
