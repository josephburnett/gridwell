# internal/local/store — the format contract

This package is the node's one database, `<home>/gridwell.db`: home's
content (`ns = ''`), every plugin's memory as namespaced rows of the same
tables, and the transport's connections. It holds node facts only: minted
ids (`ns`, `key` → id), layout, framing, the user's bytes, connections,
tombstones. What a source last said is cache and lives in `cache.db`
(`internal/sourcecache`), never here.

A plugin row is minted LAZILY: listing a source writes nothing (`Overlay` is
a read-only join, `Mint` the one INSERT), so a row exists only for an entry
the user has touched — moved, resized, framed, or pointed a reference at.
The consequence for an outage: the rows the user touched answer an
unreachable source, unchanged and stamped stale; an untouched entry has no
row and is simply absent until the source speaks again.

## The promise

Data written by any released binary stays readable forever. A newer binary
opens an older file and brings it forward, with every user-visible fact
intact.

- Never delete the DB to absorb a schema change.
- Never rename, retype, or repurpose a column or table.
- Evolution is additive by default: new columns with a default, new tables,
  new indexes.
- Dead storage may be retired. The bar is evidence: grep every read and show
  that no reader decides anything on it (a pass-through is not a reader).
  The step is a rebuild migration that preserves every surviving row and
  the `sqlite_sequence` seeds. A row the new shape cannot hold is converted,
  never deleted. Record the decision in the migration's comment. A wire
  field retired with a column becomes `reserved`. v10, v11, and v12 are the
  worked examples.

## How the schema is represented

- `columns.go` (`tilesColumns`, `gridsColumns`) is the one description of
  a column: name, SQL type and constraints, comment, the version whose data
  it carries (`since`), the `rpc.Tile`/`rpc.Grid` field it binds to when on
  the wire, and a `noCopy` reason when a clone skips it. The DDL, the
  SELECT, the scan, the clone INSERT, and every rebuild copy list derive
  from it. Only the tiles kind `CHECK` (`tilesCheck`) is literal text.
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
   and a `bind` if it is on the wire. Do not spell the name anywhere else.
2. `migrations.go`: bump `schemaVersion`; append
   `{to: N, run: addColumn("ALTER TABLE … ADD COLUMN …")}`.
3. `migration_harness_test.go`: append one `migrationFixture{version: N,
   seed, verify}`.

An on-wire column also needs its proto field (same snake_case name);
`TestDescriptorMatchesProto` fails until they agree. A derived proto field
goes on that test's `wireOnly` list with its reason. If a clone must supply
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
`externalsIndexDDL`) → restore the `sqlite_sequence` row. Without the
restore, ids of deleted tiles get reused and stored references resolve to
the wrong tile. `migrateV10` is the worked example; `rebuildSelect` is where
a retiring column's meaning converts into its successor (`view_x + w/2`).
`grids` has no `CHECK`: `DROP INDEX` then `ALTER TABLE … DROP COLUMN`.

A rebuild always materializes the current shape, so a chain-built file at
N-1 never has a retired column. A drop or conversion needs a genuine-old-file
test (`TestMigrateV10OverAGenuineV9File`), not just a fixture. Fixtures find
their rows by `alt_text`, which survives the whole chain. `Open` runs the
chain before `bootstrapRoot`.

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
  (callers: `WriteContent`'s text and url arms, `RenameTile`) claims and
  `finishContentEdit` bumps. Everything else — captures, framing, layout —
  goes through `loadForWrite` + `emitTileChanged` and neither claims nor
  bumps. Layout races resolve last-writer-wins; the overlap check in the
  same transaction is what prevents two tiles in one cell.
  `version_rule_test.go` is the table.
- Blobs are sha256-addressed, immutable, refcounted, and carry their
  `media_type`. Read it back; never hard-code a type at the read site.
