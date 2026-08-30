# Simplify — the plan of record for `docs/architecture-review.md`

Owner brief (2026-08-29): "design and implement a set of changes to
address all of your findings. Fix the shortcomings. Get to the target
state." This document is that plan: one workstream per finding, the
decisions each one rests on, and the order they land in. Each workstream
is a series of ordinary commits on `main` — test first, `make check`
green, the native gates where the native layer is touched.

The review's target shape, restated as deliverables:

| # | Workstream | Finding | Deliverable |
|---|---|---|---|
| S1 | Prune | 7 | Dead storage and dead wire/client machinery gone |
| S2 | In-process namespaces | 5 | Home and plugins are Go values; gRPC only at the export and the plugin subprocess |
| S3 | One transport | 8 | Shells ride the web door; the Electron shell stack is deleted; every host has shells |
| S4 | One framing | 2 | One float representation, one shape for doorway and root, one writer path |
| S5 | Versions on content | 3 | `version` = user content bytes only; framing unversioned LWW; one outbox |
| S6 | One Tile | 4 | Proto is the owner; `rpc.Tile` and the store column lists are derived, not mirrored |
| S7 | One memory | 6 (+7 `listings`) | One "what did the source last say" engine, outside the forever file |
| S8 | One place | 1 | A pane is a stack of frames; one descent, one ascent, one framing writeback |

## Decisions (owner decisions of this plan; reverse only with a new one)

- **Dead storage may be removed.** `internal/local/store/CLAUDE.md` said
  "never drop". The promise that matters is *data written by any released
  binary stays readable forever*; a newer binary always brings an older
  file forward. Removing storage that no released binary reads for any
  user-visible meaning does not break that promise. So: a DEAD table or
  column (verified: no reader that decides anything; no user meaning) may
  be dropped by a **rebuild migration** that preserves every surviving
  row and the `sqlite_sequence` seeds. Additive stays the default; a drop
  is an owner decision recorded in the migration's comment. Wire fields
  removed from protos are `reserved`, never renumbered.
- **The forever file holds node facts only**: ids, layout, framing, the
  user's content, connections, plugin memory of *what the node minted*
  (`ns`,`key` → id). What a source last *said* (listings, bodies,
  previews) is cache and lives in `cache.db`, one engine for plugins and
  connections alike.
- **Framing is a float center and a pane-size-independent zoom**, stored
  on the doorway tile; a root (no doorway) keeps the same shape on its
  grid row. Home's root is `ns = ''`'s grid row — `system.root_view_*`
  retires.
- **Only user content bytes carry a `version`.** Automatic captures
  (title, preview, shell title) do not bump; framing writes carry no
  claim and no bump.
- **The plugin.v1 subprocess and the federation socket are the only gRPC
  hops.** Inside the node, a namespace is a Go interface.
- **Shells are a WebSocket on the web door**, cookie-gated like every
  other page request, so the web client on any host has shells.
- **A pane's place is one stack of frames** `(gridID, doorTileID,
  viewport)`; a descent pushes, an ascent pops; the URL and the pane
  layout blob are encodings of that stack, never a second model.

## Order

Waves are ordered by dependency; within a wave the streams touch
disjoint code and run in parallel.

1. **S1 Prune ∥ S3 One transport** — independent; S1 shrinks what every
   later stream has to carry.
2. **S2 In-process namespaces ∥ S4 One framing** — S2 is server plumbing,
   S4 is store + client framing.
3. **S5 Versions on content ∥ S6 One Tile + S7 One memory** — S5 rewrites
   the client persistence dispatch on the simplified store.
4. **S8 One place** — last, on the simplified base, under the e2e gate.
5. **Docs** — `ARCHITECTURE.md`, `CLAUDE.md` owner decisions, the store
   contract; the review's findings marked resolved.

## Status: DONE 2026-08-29

Every workstream landed on `main` in `10c009a..7cf468f`, in the planned
order, each a series of ordinary commits with `make check` green and the
native gates where the native layer was touched. Per-finding detail — the
mechanism now in force, and what each deviation was for — is the
**Resolution** section of `docs/architecture-review.md`.

| # | Range | Landed | Deviation from this plan |
|---|---|---|---|
| S1 Prune | `0a42c34..5279a6b` | The unconfigured plugin well's verbs, `create_schemas`, menu-entry CREATION; schema **v10** drops `session`, `tiles.configure_plugin_id` and `object_id` | `listings` moved with S7 (schema v12) rather than here — its last reader was the adapter's listing memory, which S7 owned |
| S2 In-process namespaces | `9be7b4d..29eeda1` | `internal/namespace` as Go values; `compose.ServeInProcess` and the second router deleted; `Registry.SetTransit`, `InfoResponse.transit` (reserved 15) and `internal/plugin/proxytest` retired | none. The subprocess door was deliberately not touched |
| S3 One transport | `e1a4a01..802c09f` | The `/shell` WebSocket on the web door; the Electron shell stack deleted; `caps.LiveShell` = `!shellsDisabled`; a browser shell proved end to end (`web-shell.spec.ts`) | `mobile.Start` still sets `disable_shells` — what a phone cannot HOST is not what its client cannot REACH |
| S4 One framing | `27bb535..622f8e6` | Schema **v11**: a float centre on the doorway row (`tiles.view_cx/cy/zoom`) or the root grid row (`grids.root_cx/cy/zoom`); one `SetFraming`, one `Store.SetFraming`, one `persistFraming`; `system.root_view_*` and the quantization math gone | `SetTile`'s well arm became a REFUSAL naming `SetFraming`, not a deletion, so the kind→operation mapping stays total |
| S5 Versions on content | `fe69fb4..b4bd5c0`, `00434b6` | `claimContentVersion`/`loadForWrite` make the rule structural; the retired claim fields are `reserved`; `client/pending` + the dirty ledger become `client/outbox` | `SetTileRequest.version` (its one reader is the rename arm) and `WriteContentRequest.version` stay, with the pane arm's ignored claim pinned as such. LAYOUT was moved to "no claim" on evidence — the plan only named framing |
| S6 One Tile | `b1be3fe..71f6976` | `api/rpc/wire_gen.go` generated from the proto; `store/columns.go` the one column descriptor; the derived wire fields inventoried with one owner each | "use `pb` types everywhere" rejected on measurement (same binary size; copylocks would force 197 value uses into shared pointers). The Event oneof, the embedded Framing and the per-kind sugar stay hand-written |
| S7 One memory | `d3e081d..8a39b01` | `internal/sourcecache`: one engine, one `cache.db`, prefetch as per-namespace policy, in front of every non-home namespace; the adapter's listing memory deleted; schema **v12** drops `listings` | The fold is NOT "cache the adapter's answer": a dark SOURCE is answered by the durable rows (a move made during the outage survives), a dark PLUGIN by the cache |
| S8 One place | `cc44fd3..2a52dc0` | One `pane.Stack` of frames; `client/url` and `client/workspace` folded in; eight ascents and five descents become one `ascend` and one `descend` | The PANE-TILE level stays a second axis (`pane.Levels`), because swapping the whole pane tree is a different gesture |
| Docs | this workstream | `CLAUDE.md` owner decisions, `ARCHITECTURE.md` read end to end for contradictions, the store contract, the review's Resolution, and the stale comments across the tree | none |

Bugs found and fixed along the way, none of them the point of their
workstream: a clone had been silently dropping `content_zoom`,
`url_history` and `alt_user` (S6); `Open` ran `bootstrapRoot` before the
migration chain (S1); a rebuild did not recreate v9's
`idx_tiles_live_key` (S1); a shell takeover never repainted, which the old
single-IPC-queue ordering had been hiding (S3); the shell door raced its
own exit frame against the teardown (S3); the unload flush knew only about
FRESH writes, so anything an earlier outage had parked died at quit (S5);
`pluginhealth` read an id's SHAPE to recover a fact the row declares (S1);
and a `mountcache` test restored the process logger to nil, panicking
other tests in the package (S3).

### After the gates (2026-08-30)

The full native gate run on the merged tree found seven e2e failures that
a bisect against the pre-program tree proved were ours, not the box's:
five specs still passed the `objectId` S1 removed from
`WebviewRegistry.place()` (`8d068ad`), and S4 had moved every
never-visited grid half a footprint (`7dd7c4a`, `zoomtrans.EffectiveCenter`)
while two specs read the retired origin field names (`66e7c2c`; those names
are now retired words, so `make check` catches the class). Gap closed:
S1 and S4 touched the native layer without running the native gates.
