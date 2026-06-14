# Gridwell storage format

This document is the contract for Gridwell's on-disk format. Its goal is a
**durable, archival, backward-compatible** database: you can shut Gridwell
down, copy `gridwell.db` somewhere safe, and trust that — barring a serious
application bug — your data is intact and will keep opening in future
versions. Once the format is declared frozen (see *Versioning* below), every
schema change must preserve the ability to read data written from that point
on.

This spec is referenced in-band: the main database stamps a `PRAGMA
application_id` identifying it as a Gridwell file and a `PRAGMA user_version`
naming the schema generation, and a `meta` row points future readers here.

## The guiding split: owned vs. projected

Gridwell stores two fundamentally different kinds of data, and the format
keeps them in **two physically separate SQLite files**:

| | `gridwell.db` (**durable / archive**) | `gridwell-cache.db` (**ephemeral / projected**) |
|---|---|---|
| Contains | User-authored canvas: regular grids, tiles, content blobs, **frozen previews** | Host mirrors: filesystem listings, the process tree, sticky arrangement of those |
| Source of truth | Itself | The host (a directory, the process table) |
| Survives copy to another machine | **Yes — this is the archive** | No — regenerated from the host |
| Survives deletion | Must not be lost | Rebuilt on demand; safe to delete |

The two files are opened on **one connection** via `ATTACH DATABASE
'gridwell-cache.db' AS cache`, so a single SQLite transaction spans both
atomically. The main file never references the cache file with a foreign key
(SQLite can't enforce cross-file FKs anyway); the link is by **identity**
(an exit well stores its `fs_path` / `pid`; the cache grid is looked up by
`(source_kind, source_id)`).

**You can copy `gridwell.db` at any time the application is shut down and
have a complete archive.** The cache file is disposable.

### Globally-unique ids across the two files

Both files have `AUTOINCREMENT` `grids` / `tiles` / `blobs` tables, so their
id spaces would collide (both start at 1). To keep every id self-routing, the
**cache file's autoincrement sequences are pre-seeded to a high base**
(`cacheIDBase = 1_000_000_000_000`). Therefore:

- `id <  cacheIDBase` → the row lives in `main`.
- `id >= cacheIDBase` → the row lives in `cache`.

`cacheIDBase` sits comfortably above any realistic main id and below 2⁵³ (the
JSON/JS-safe integer ceiling the wasm client relies on). The client treats
all ids uniformly; it never needs to know there are two files. The server
routes each `loadGrid` / `loadTile` / `GetBlob` to the right schema by id
range (`tilesTbl(id)` / `gridsTbl(id)` / `blobsTbl(id)` pick `main` or
`cache.`).

### How an exit well points across the file boundary (stage 2b mechanics)

A `file-well` / `process-well` in a *main* grid keeps storing its child
source-grid id in `child_grid_id` — but that id now names a **cache** grid
(≥ `cacheIDBase`). So:

- The `tiles.child_grid_id` **foreign key is dropped** (SQLite can't FK across
  attached files; a regular `well`'s child is still a main grid, and the
  refcount machinery + property test remain its integrity net).
- The wire is unchanged: `child_grid_id` stays populated, so the client
  descends exactly as before; `GetGrid(childId)` routes to the cache file.
- Cache source grids are **create-on-demand and never individually GC'd**
  (`getOrCreateSourceGrid` = find-or-create; cache is disposable, so an
  orphaned source grid is harmless and cleared by deleting the cache file).
  Exit wells therefore do **not** refcount their child source grid
  (`tileRefs` returns no grid ref for them — only their durable preview blob);
  only regular `well`s refcount their main child grid.
- **Self-heal across a cache wipe:** because cache persists across runs, a
  stored `child_grid_id` normally stays valid. If the cache file is deleted
  and rebuilt, a stale `child_grid_id` won't resolve; navigation
  (`buildGridSequence`) detects the miss, re-resolves the source grid by
  identity (`fs_path` / `pid` → `getOrCreateSourceGrid`), and rewrites
  `child_grid_id` to the fresh cache id. Identity (`fs_path` / `pid`), not the
  id, is the durable link.

Reconciler writes (the synthesized file/dir/process tiles, the `@info`
markdown blob) all target the cache schema, keyed off the source grid's cache
id. The main file thus only ever holds authored content + durable previews.

## The durability rule

> **A tile's durable preview is stored in the main DB if and only if the tile
> lives in a regular (non-source) grid.**

Durability follows the **parent grid**, not the tile kind:

- A `file-well` / `process-well` placed in a **regular Gridwell grid** — whether
  dropped from the palette (the root "files" / "processes" wells) or
  clone-linked (dashed) out of a source grid — is *being viewed inside
  Gridwell proper*. It carries a **frozen preview** in the main DB. Open the
  archive on another machine and you still see what you saw. If the backing
  host path/PID exists, the live view replaces the frozen one; if it's gone,
  the frozen preview stands ("things stay where you put them").
- The **contents of a source grid** — the directory listing, the child
  processes, the synthetic `@info` tile, and crucially their **sticky
  arrangement** (the `x,y` you dragged them to) — live entirely in the cache
  file. They are projections of host state and are *not* part of the archive.

`url` and `shell` tiles already carry frozen JPEG previews; those are durable
data (they are screenshots), full stop — even after a reboot kills the live
session, the last frame is kept.

### Worked example

You drop a `file-well` at `/home/joe` into a root grid, descend, rearrange the
subdirectories, ascend. Result:

- Main DB: the `file-well` tile (`fs_path = /home/joe`), its placement, and its
  frozen preview blob.
- Cache DB: the `fs` grid for `/home/joe`, one tile per entry, with the
  arrangement you set.

Copy `gridwell.db` to a laptop that has no `/home/joe`: you see the frozen
preview of the well, exactly as you left it. Copy it to a machine that *does*
have `/home/joe`: descending reconciles against the real directory and shows
the live listing (in a fresh cache file), but with default auto-layout, since
the arrangement was never in the archive.

## Versioning — canonical, PRAGMA-based

Versioning uses SQLite's built-in header slots, not a hand-rolled table:

- **`PRAGMA application_id = 0x4757654C`** ("GWeL", big-endian ASCII) on the
  main file — marks it as a Gridwell DB for `file(1)`, archival tooling, and
  our own open-time sanity check.
- **`PRAGMA user_version = N`** on the main file — the schema generation.

On open:

1. If `application_id` is unset and the DB is empty → fresh DB: stamp
   `application_id` and `user_version = current`.
2. If `application_id` is set but isn't Gridwell's → refuse to open (not our
   file).
3. If `user_version > current` → refuse (an older binary must not misread a
   newer schema).
4. If `user_version < current` → run each migration `v → v+1` in order
   (additive only), then stamp `current`.

### Migration policy (post-freeze)

Until the format is declared **frozen**, Gridwell is in *testing mode*: schema
changes go straight into the schema and the user wipes the DB (no migration
needed). The migration *framework* is built now so the discipline is in place
before the promise is made.

After freeze, every change is a numbered migration and must be **additive and
non-destructive**:

- Add columns (with defaults) and tables; never drop, rename, or repurpose an
  existing column. A column's meaning is fixed forever.
- A new tile kind adds its columns and a `CHECK` clause via a migration.
- Old data must remain readable. The "refuse newer" guard stays.

## Tables (target schema)

### Main file (`gridwell.db`) — durable

- `system` — KV singletons: `root_grid_id` and the root viewport
  (`root_view_cx/cy`, `root_zoom`). Schema version lives in `user_version`,
  not here.
- `meta` *(deferred — optional)* — self-description beyond what the PRAGMAs
  carry: writer build, format-doc pointer, db-created-at. Held back for now;
  `application_id` + `user_version` are the canonical identity/version and
  suffice. Add only if a concrete need appears.
- `grids` — regular Gridwell grids only. No `source_kind` / `source_id` (those
  are a cache-only concept). Carries `created_at` and `updated_at`.
- `tiles` — tiles in regular grids: all seven kinds can appear here. Exit
  wells (`file-well` / `process-well`) here carry `fs_path` / `pid` and may
  carry a `preview_blob_id` (their durable frozen preview). Carries
  `created_at` and `updated_at`.
- `blobs` — content-addressed (sha256), refcounted, durable: markdown source,
  frozen JPEGs (url / shell / well previews). Self-describing via a
  `media_type` column. Carries `created_at` (blobs are immutable — created or
  GC'd, never mutated).

### Cache file (`gridwell-cache.db`) — ephemeral

- `grids` — source-backed grids: `source_kind` ∈ {`fs`, `proc`}, `source_id`
  (path or PID), unique per `(source_kind, source_id)`. Ids seeded ≥
  `cacheIDBase`.
- `tiles` — the reconciled, arranged contents of those grids (file/dir tiles,
  process-well tiles, the `@info` tile), including their sticky `x,y`.
- `blobs` — ephemeral content for cache tiles (e.g. the `@info` markdown). Kept
  out of the durable file so the archive holds only authored content.

### Timestamps

`updated_at` is maintained on every meaningful row (`grids`, `tiles`) even
though nothing reads it yet: a planned "jump to recently-touched data" feature
will. Blobs are immutable, so they carry `created_at` only.

## Blob self-description

Every blob row records a `media_type` (`text/markdown`, `image/jpeg`, …) so a
blob is interpretable on its own — important if blobs are ever exported or
inspected outside the referencing tile. The referencing column still encodes
intent (`blob_id` = content, `preview_blob_id` = frozen image), but the blob no
longer depends on that context to be understood.

## Archival hygiene

The main file is a clean, self-contained archive when the app is shut down.
Operationally:

- WAL mode is on for runtime concurrency; a clean shutdown checkpoints it. A
  `prepare-archive` routine (`wal_checkpoint(TRUNCATE)` + `VACUUM`) produces a
  single sidecar-free `.db`.
- `integrity_check` + `foreign_key_check` validate the file (cheap on open,
  available on demand). Manual refcount GC + FKs make `foreign_key_check` a
  real safety net.

## Implementation stages

Each stage is independently committable with `make check` green. Until the
format is frozen, schema changes are clean breaks (wipe the DB); the migration
framework is built but carries no historical migrations yet.

0. **Canonical versioning & identity.** `application_id` + `user_version`;
   move schema version out of the `system` table; build the additive-migration
   runner (with the open-time guards). (`meta` table deferred — the PRAGMAs
   are the canonical identity/version.)
1. **Timestamps & blob self-description.** `grids.updated_at`, `blobs.created_at`,
   `blobs.media_type`; plumb `media_type` at every `putBlob` call.
2. **Physical separation.** The largest stage, split:
   - **2a (done).** ATTACH `gridwell-cache.db`; prefix-parameterized table
     DDL; seed cache id base; id-range partitioning (`isCacheID`). No behavior
     change yet.
   - **2b.** Route `getOrCreateSourceGrid` and the reconciler into the cache
     schema so `fs`/`proc` grids + their tiles + arrangement + `@info` blobs
     live in the cache file; drop the `child_grid_id` FK; range-route every
     id-keyed load / refcount / blob op (`tilesTbl`/`gridsTbl`/`blobsTbl`);
     stop refcounting source grids from exit wells; add self-healing
     re-resolution in `buildGridSequence` for a wiped cache. (Mechanics
     above.) Gated by the existing source-grid + COW property tests.
3. **Durable exit-well previews (format).** Relax the `tiles` `CHECK` so
   `file-well` / `process-well` rows in regular grids may hold a
   `preview_blob_id`; render the frozen preview as the fallback when the host
   path/PID is absent.
4. **(Follow-on) Well-preview capture pipeline.** Render a file/process-well's
   Gridwell view to a JPEG and freeze it on ascend (the URL/shell freeze
   analog). Out of scope for format stabilization; the format already holds it.
