# internal/local/store — the home persistence layer

This package is the SQLite-backed store behind the node's native `home`
kind (`internal/local`; né localdb): the user's text, URLs, and grids.
**The home format is out of testing mode. Its v1 schema is frozen and the
forward-compatibility promise is in effect.**

## The promise

Data written by any released gridwell binary stays readable **forever**. A newer
binary always opens an older DB and brings it forward; it never requires the
data to be thrown away.

- **Never delete the DB to absorb a schema change.** That was the testing-mode
  habit; it is now forbidden for the home store.
- **Never drop, rename, retype, or repurpose an existing column or table.** Old
  rows were written against the old meaning and must keep reading back the same.
- Evolution is **additive only**: new columns (with a default), new tables, new
  indexes. That is the whole vocabulary.

## How the schema is represented

- `schema.go` `tablesTemplate` (returned by `tablesDDL()`) is the **single
  readable description of the current schema** — the latest shape a fresh `Open`
  materializes directly. Read it to know the present columns; do not reconstruct
  the schema by replaying migrations.
- `schema.go` `tablesV1` is the **frozen v1 base** — an immutable byte-for-byte
  copy of `tablesTemplate` as it was when the format froze. **Never edit it.**
  Tests build genuine "old files" from it and migrate them forward.
- `migrations.go` `migrations` is the ordered, additive chain; entry _i_ takes a
  DB from version _i+1_ to _i+2_. `schemaVersion` is the current generation,
  stamped into the SQLite header as `user_version`. The engine that applies
  the chain (fresh-stamp / foreign-file refusal / newer-version refusal) is
  the shared `internal/dbformat.EnsureVersion` — one implementation for every
  node DB (this store, each provider's memory DB in `internal/layout`, the
  mount cache in `internal/plugin/mountcache`); `migrateUp` here is a thin
  adapter.

`TestSchemaEquivalence` proves `tablesV1 + migrations == fresh tablesTemplate`.
That equivalence is what lets `migrateUp` stamp a fresh DB without running the
chain: the two routes are guaranteed to converge.

## Adding a column (the common case)

Two edits plus one fixture — the equivalence test makes any omission a loud
failure, so you cannot half-do it:

1. **`schema.go`**: add the column inline to `tablesTemplate` (keeps the
   readable latest shape true). Do **not** touch `tablesV1`.
2. **`migrations.go`**: bump `schemaVersion` by one and append
   `{to: N, run: addColumn("ALTER TABLE … ADD COLUMN …")}`.
3. **`migration_harness_test.go`**: append one `migrationFixture{version: N,
   seed, verify}` — seed rows valid at version N-1, verify the new column is
   present and the old rows survived.

If the column is storage-only (not on the wire), also add its name to the
tiles/grids `storageOnly` allowlist in `drift_test.go`; otherwise add it to the
proto and the drift lint passes on its own.

### SQLite `ADD COLUMN` limits

A `NOT NULL` column must carry a **constant** default (`… NOT NULL DEFAULT 0`,
`DEFAULT ''`). No `UNIQUE`, no `PRIMARY KEY`, no non-constant default
(`CURRENT_TIMESTAMP`) on an added column. New tables and indexes ride in for
free via `CREATE … IF NOT EXISTS` in `tablesDDL()` (they appear on old DBs at
Open), but still bump `schemaVersion` and add a migration + fixture so the
version bookkeeping and equivalence stay honest.

## The expensive case: changing a CHECK / a tile kind

`ALTER TABLE ADD COLUMN` **cannot** change the table-level `CHECK` constraint on
`tiles` (the `kind IN (…)` / per-kind column rules). Adding a new tile kind
therefore needs a **table-rebuild migration**, all inside the migration tx:
create `tiles_new` with the new CHECK → `INSERT INTO tiles_new (explicit
columns, id included) SELECT … FROM tiles` → `DROP TABLE tiles` → `ALTER TABLE
tiles_new RENAME TO tiles` → recreate the three `idx_tiles_*` indexes — **and
save/restore the `sqlite_sequence` row for `tiles`**: `DROP TABLE` deletes it,
and the copy re-seeds at the max *surviving* id, so without the restore the ids
of previously-deleted tiles get REUSED (violating the identity invariant below;
embeds and deep links would resolve to the wrong tile). The v5 migration
(`rebuildTilesForPaneKind`, the first executed rebuild) is the worked example:
it builds `tiles_new` from the same `tilesTableDDL` text a fresh Open uses (one
DDL source, no drift), and its fixture pins the id-reuse trap. The equivalence
and chain tests still guard the result. Reach for this only when a CHECK must
change.

## Test discipline (must stay green)

- Every migration adds **exactly one** `migrationFixture` (enforced by
  `TestMigrationsWellFormed`).
- `TestSchemaEquivalence`, `TestMigrationChain`, `TestPerMigration`, and
  `TestReopenRoundTrip` must stay green.
- Migration and durability tests run on **file-backed** DBs (`newTestStoreFile`,
  `buildDBAtV1`): a `:memory:` DB forces `journal_mode=memory`, so WAL and the
  pinned `synchronous` level — the durability we are proving — are inert there.

## Durability

`Open` pins connection-scoped pragmas every time (`journal_mode=WAL`,
`synchronous=NORMAL`, `foreign_keys=ON`). `synchronous` is not stored in the
file and defaults to FULL, so it is re-applied on every Open; NORMAL is durable
against app/OS crashes (a power loss can lose at most the last uncheckpointed
transaction, never corrupt the file). Never relax these in a way that weakens an
existing file's guarantees.

## Identity invariants (don't break these)

- Grid/tile/blob ids are `AUTOINCREMENT` and **never reused** — client caches
  and stored references (embeds, deep links) are keyed by id.
- `version` bumps on content edits only; framing writes (view_*, text scroll)
  must not bump it. The split lives in `finishContentEdit` vs `emitTileChanged`.
- Blobs are content-addressed (sha256), immutable, refcounted, and
  self-describing via `media_type` (read it back; never hard-code a type at the
  read site).
