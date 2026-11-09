# internal/local/store — the format contract

This package is the node's one database, `~/.gridwell/gridwell.db`: home's
content, the files' rows in the same tables, and the transport's
connections. It holds node facts only: minted ids, layout, framing, the
user's bytes, connections, tombstones. What a connection last answered is
cache and lives in `cache.db` (`internal/sourcecache`), never here.

`key` is the one column that tells the two sides apart, on tiles and grids
alike: '' on every home row, a file's or directory's absolute path on the
files' (`side` in files.go, `FileRows`). A home row's grid may be a
directory's keyed row: a Gridwell tile over a directory, its grid spelled
`~<path>` on the wire (`scanHomeTile`, `gridRow`).

A file's row is minted lazily: listing a directory mints nothing
(`Overlay` is a read-only join, `Mint` the one INSERT), so a row exists only
for a file the user has touched — moved, resized, framed, renamed, or
pointed a link at. A listing writes only to rows that exist: `Refresh`
keeps a touched row's snapshot at what the disk last said, and `Sweep`
tombstones a row whose path a listing no longer names. While a directory
will not read, the rows the user touched answer, unchanged; an untouched
file has no row and is absent until the directory reads again. A symlink
to a file is a link row (`link_target_id` holds the target's path id), and a
row whose file becomes a link is converted in place by `Refresh`, keeping
its id and placement.

The Gridwell tiles over a directory are home rows on grids keyed at or
under its path (`overlay.go`): a move of the directory re-keys them with
the files' rows under it (`Rekey`), and its delete sends them to home's
trash (`TrashOver`). A directory gone from disk with Gridwell tiles still
over it is listed from these rows (`HeldUnder`).

`gridwell size` reads the file through `OpenReadOnly`, which writes
nothing and may run beside a serving node, and `Analysis`. `gridwell
clean` runs `Clean`: one `withMutation` that removes what nothing reaches,
through the delete's own teardown, refused whole if a kept row or blob
would change. The trash and every tombstone are kept.

## The promise

Data written by any released binary stays readable forever. A newer binary
opens an older file and brings it forward, with every user-visible fact
intact.

- Never delete the DB to absorb a schema change.
- Never rename, retype, or repurpose a column or table. v23's rename of
  `grids.context_key` to `key` is the one decided exception.
- Evolution is additive by default: new columns with a default, new tables,
  new indexes.
- Dead storage may be retired. The evidence is a grep of every read showing
  that no reader decides anything on it (a pass-through is not a reader).
  The step is a rebuild migration that preserves every surviving row and
  the `sqlite_sequence` seeds. A row the new shape cannot hold is converted,
  never deleted. Record the decision in the migration's comment. A wire
  field retired with a column becomes `reserved`. v10, v11, and v12 are the
  worked examples; v23 is the one decided exception to converting, the
  rows of every namespace but home's dropped.

## How the schema is represented

- `columns.go` (`tilesColumns`, `gridsColumns`, `connectionsColumns`) is the
  one description of a column: name, SQL type and constraints, comment, the
  version whose data it carries (`since`), the `gridwellv1.Tile`/`gridwellv1.Grid` field it
  binds to when on the wire, and a `noCopy` reason when a copy skips it. The
  DDL, the SELECT, the scan, the copy INSERT, and every rebuild copy list
  derive from it. Only the tiles kind `CHECK` (`tilesCheck`) is literal text.
- `connections` is in the chain like everything else, adopted by v13. The
  store owns its shape and its queries (`connections.go`), and writes its
  rows through `withMutation` like every other node fact;
  `internal/connection` decides what to write and holds no handle. The `system` KV
  table and `_gridwell_meta` stay outside: neither carries a user fact.
- `schema.go` `tablesDDL()` renders the descriptor: the shape a fresh
  `Open` materializes. `tablesV1` is the frozen v1 base. Never edit it.
  Tests build genuine old files from it and migrate them forward.
- `migrations.go` `migrations` is the ordered chain; entry _i_ takes a DB
  from version _i+1_ to _i+2_. `schemaVersion` is stamped as
  `user_version`. `internal/dbformat.EnsureVersion` applies the chain for
  every node DB.

`TestSchemaEquivalence` proves `tablesV1 + migrations == fresh tablesDDL()`.

## Adding a column

1. `columns.go`: append one entry — `name`, `ddl`, `comment`, `since: N`,
   and a `bind` if it is on the wire. Do not write the name anywhere else.
2. `migrations.go`: bump `schemaVersion`; append
   `{to: N, run: addColumn("ALTER TABLE … ADD COLUMN …")}`.
3. `migration_harness_test.go`: append one `migrationFixture{version: N,
   seed, verify}`.

An on-wire column also needs its proto field (same snake_case name);
`TestDescriptorMatchesProto` fails until they agree. A derived proto field
goes on that test's `wireOnly` list with its reason. If a copy must supply
a value, `insertTileCopy`'s value map is where; `copyBinding` errors by name
until it does.

SQLite limits: an added `NOT NULL` column needs a constant default; no
`UNIQUE`, `PRIMARY KEY`, or non-constant default. New tables and indexes
ride in through `CREATE … IF NOT EXISTS` but still get a migration and
fixture.

## Rebuilding a table

Changing the tiles `CHECK` or retiring storage needs a rebuild, inside the
migration tx: create `tiles_new` from the current DDL → `INSERT … SELECT`
the columns `rebuildColumns(N-1)` derives → `DROP TABLE tiles` → rename →
recreate every `idx_tiles_*` index (from `tilesIndexDDL` and
`tilesKeyIndexDDL`) → restore the `sqlite_sequence` row. Without the
restore, ids of deleted tiles get reused and stored links resolve to
the wrong tile. `migrateV10` is the worked example; `rebuildSelect` is where
a retiring column's meaning converts into its successor (`view_x + w/2`).
`grids` has no `CHECK`: `DROP INDEX` then `ALTER TABLE … DROP COLUMN`.

A rebuild always materializes the current shape, so a chain-built file at
N-1 never has a retired column. A drop or conversion needs a genuine-old-file
test (`TestMigrateV10OverAGenuineV9File`), not just a fixture, and a drop
that picks rows by a column needs a test over a file an earlier rebuild in
this binary took it from: v23 picks a foreign namespace's tile by its grid's
`ns` too, because a file left at v9 to v20 has no `tiles.ns`
(`TestMigrateV23DropsAKeylessPluginTileByItsGrid`). Fixtures find
their rows by `alt_text`, which survives the whole chain. `Open` runs the
chain before `bootstrap`.

## Tests that must stay green

`TestMigrationsWellFormed` (one fixture per migration),
`TestSchemaEquivalence`, `TestMigrationChain`, `TestPerMigration`,
`TestReopenRoundTrip`. Migration and durability tests use file-backed DBs; a
`:memory:` DB forces `journal_mode=memory` and proves nothing about WAL.

## Durability

`Open` pins `journal_mode=WAL`, `synchronous=NORMAL`, `foreign_keys=ON` on
every open. Never relax them.

## Invariants

- Ids are `AUTOINCREMENT` and never reused.
- `version` means the user's content bytes changed. `claimContentVersion`
  (callers: `WriteContent`'s text, url and workspace arms, `RenameTile`) claims and
  `finishContentEdit` bumps. The bump (`bumpTileVersion`) is the one writer
  of `content_at`, so a row's content time moves exactly when its version
  does; `created_at` and `updated_at` (every write) are the other two times. Everything else — captures, framing, layout —
  goes through `loadForWrite` + `emitTileChanged` and bumps nothing; a
  placement claims the placement it saw (`ClaimPlacement`, home's and the
  files' rows alike), a freeze claims the freeze it saw (`setFrozenTx`),
  and a delete claims the version and the placement (`DeleteClaim`). Those single-tile writes run in `withTileWrite`, so one that lands
  where the row already is writes nothing and tells no one
  (`repeat_write_test.go`). A stale placement is refused; the overlap check in the
  same transaction is what prevents two tiles in one cell.
  `version_rule_test.go` is the table.
- Blobs are sha256-addressed, immutable, refcounted, and carry their
  `media_type`. Read it back; never hard-code a type at the read site.
