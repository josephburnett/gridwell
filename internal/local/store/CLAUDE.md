# internal/local/store — the home persistence layer

This package is the node's ONE database (`<home>/gridwell.db`,
docs/one-node.md §2.6): home's content — the user's text, URLs, and
grids (`internal/local`; né localdb) — and, since schema v9, every
content plugin's memory as namespaced rows of the same tables
(`external.go`; `ns = ''` is home) plus the transport's connections.

**The forever file holds NODE facts only** (owner decision,
docs/simplify-plan.md S7): ids the node minted (`ns`,`key` → tile/grid
id), layout, framing, the user's content bytes, connections, and
tombstones. What a SOURCE last said — a plugin's listing, a remote's
tiles, bodies, previews — is cache: it lives in the disposable
`<home>/cache.db` behind `internal/sourcecache`, never here. That is
what schema v12 retired the `listings` table for. A dark source costs
nothing in this file: the adapter merges an empty non-authoritative
listing and the durable rows answer, unchanged.
**The home format is out of testing mode. Its v1 schema is frozen and the
forward-compatibility promise is in effect.**

## The promise

Data written by any released gridwell binary stays readable **forever**. A newer
binary always opens an older DB and brings it forward; it never requires the
data to be thrown away.

- **Never delete the DB to absorb a schema change.** That was the testing-mode
  habit; it is now forbidden for the home store.
- **Never rename, retype, or repurpose an existing column or table.** Old rows
  were written against the old meaning and must keep reading back the same.
- Evolution is **additive by default**: new columns (with a default), new
  tables, new indexes. Reach for anything else only for the case below.

### Retiring dead storage (owner decision, docs/simplify-plan.md 2026-08-29)

The promise is that data written by a released binary stays **readable** —
that a newer binary always brings an older file forward with every
user-visible fact intact. Storage that no released binary reads for any
user-visible meaning carries no such fact, so it may be **retired**:

- The bar is evidence, not opinion: grep every read (store, clone, link,
  conv, server, client, tests) and show that no reader DECIDES anything on
  it. A pass-through copy is not a reader; a test asserting the value
  survives a round-trip is not a reader either.
- The step is a **rebuild migration** (below) that preserves every surviving
  row and the `sqlite_sequence` seeds. A row the new shape can no longer
  hold is CONVERTED, never deleted — the guiding rule ("things stay as you
  left them") outranks the tidiness of the drop. v10's stale unconfigured
  plugin wells are the worked example: each is given a fresh empty child
  grid so the user's tile stays where they put it.
- The decision is **recorded in the migration's chain-entry comment**, naming
  what died and when its last reader went. v10 is the first one; v11 is the
  second (`tiles.view_x/view_y`, the integer window ORIGIN, replaced by the
  float `view_cx/view_cy` center it was only ever read to compute); v12 is
  the third (the `listings` table — a CACHE that had been living under the
  frozen promise, whose one engine is now `internal/sourcecache`).
- A retiring column whose MEANING lives on is **converted, not dropped**:
  the rebuild's copy list names the DESTINATION columns and `rebuildSelect`
  supplies the expression that reads them out of the old shape
  (`view_x + w/2`). The conversion therefore happens exactly once, whichever
  rebuild step an old file passes through first.
- A wire field removed alongside a column gets `reserved <n>` in the proto —
  numbers are never reused. (`TestProtoMatchesDDL` pairs columns with proto
  fields, so a column and its field must retire in the SAME change.)
- A **rebuild always materializes the current `tilesTableDDL`**, so an older
  rebuild step replays onto the latest shape: after a drop, every earlier
  rebuild's copy list must stop naming the dropped column. The chain and a
  fresh Open still converge — `TestSchemaEquivalence` proves it.

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
  node DB (this store — home AND every plugin's memory, since v9 — and the
  source cache in `internal/sourcecache`); `migrateUp` here is a thin
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
tiles_new RENAME TO tiles` → recreate the `idx_tiles_*` indexes (BOTH
sources: `tilesIndexDDL` and, at or after v9, `externalsIndexDDL`'s
`idx_tiles_live_key`, which `DROP TABLE` takes with it) — **and
save/restore the `sqlite_sequence` row for `tiles`**: `DROP TABLE` deletes it,
and the copy re-seeds at the max *surviving* id, so without the restore the ids
of previously-deleted tiles get REUSED (violating the identity invariant below;
embeds and deep links would resolve to the wrong tile). The v5 migration
(`rebuildTilesForPaneKind`, the first executed rebuild) is the worked example:
it builds `tiles_new` from the same `tilesTableDDL` text a fresh Open uses (one
DDL source, no drift), and its fixture pins the id-reuse trap. `migrateV10` is
the worked example for a DROP (and for `grids`, which has no CHECK: `DROP
INDEX` then `ALTER TABLE … DROP COLUMN`, so the table is never dropped and its
seed is never disturbed). The equivalence and chain tests still guard the
result. Reach for a rebuild only when a CHECK must change or storage retires.

**Fixture handles.** A rebuild does not renumber rows, but it does drop
columns, so a fixture must find its rows again by a column that survives the
whole chain — `alt_text` today. (`object_id` was the handle until v10 retired
it.)

**A fixture cannot see a retired column.** Every rebuild materializes the
CURRENT shape, so a CHAIN-built file at version N-1 already has version N's
columns and none of the ones N drops — a per-migration fixture can never
plant a row in the old shape. The genuine-old-file tests
(`TestMigrateV10OverAGenuineV9File`, `TestMigrateV11OverAGenuineV10File`)
put the retired shape back by hand and run the step over it. A drop or a
conversion needs one of those, not just a fixture.

The exception is a whole TABLE created by a MIGRATION LITERAL rather than
by the template: v9 spells `listings` in its own step, so a chain-built
v11 file really has the table and the v12 fixture really plants the
retired shape. Check which it is before writing a second test.

**Open's order.** `Open` runs the migration chain BEFORE `bootstrapRoot`:
bootstrap is a write through the CURRENT column set, and an old file does not
have that shape until the chain has run.

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
- `version` means ONE thing: **the user's content bytes changed** — a text
  body, a url the user typed, a name the user typed. It is the
  optimistic-concurrency claim for exactly those edits (owner decision
  2026-08-29, docs/simplify-plan.md S5). Three consequences, pinned as a
  table by `version_rule_test.go`:
  - **Content** bumps and claims. The claim is `claimContentVersion`, whose
    only callers are `WriteContent`'s text and url arms and `RenameTile`;
    the bump is `finishContentEdit`. Who can reach those two functions IS
    the rule.
  - **Captures** — a page title, a preview jpeg, a url history, a shell's
    foreground command — neither bump nor claim. They are facts the server
    OBSERVED; they ride the tile event to every client as last-writer-wins.
    A capture that bumped could cost a concurrent editor their claim, and a
    capture that claimed could be refused for losing a race it never
    entered.
  - **Framing** (view_*, text window and mode, content zoom, url freeze,
    pane layout) and **layout** (place / move / resize / clone / delete)
    neither bump nor claim. Layout is an explicit act on a tile the user can
    see, so a race resolves as "whoever moved it last moved it"; the one
    thing a race could corrupt — two tiles in one cell — is refused by the
    overlap check inside the same transaction, claim or no claim. These go
    through `loadForWrite` + `emitTileChanged`.
- Blobs are content-addressed (sha256), immutable, refcounted, and
  self-describing via `media_type` (read it back; never hard-code a type at the
  read site).
