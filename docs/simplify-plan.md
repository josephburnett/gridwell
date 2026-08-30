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
