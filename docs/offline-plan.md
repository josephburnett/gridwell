# Offline: the implementation plan

Decided direction (Joe, 2026-08-14), narrowing `docs/offline.md` to models
E + F:

- **Every full client is a node.** Electron already ships the server as a
  sidecar; the Flutter app gets an embedded Go node (gomobile). A client's
  own writes always land in its own localdb — durable, authoritative, no
  write queue anywhere.
- **Remotes are read-only from a cache when unreachable.** A read-through
  cache lives in the LOCAL NODE (not the client), in front of every mount.
  Online it passes through and remembers; offline it serves what it
  remembered, visibly stale. Writes to a remote always pass through and
  fail visibly when the mount is down.
- **The web client stays online-only.** A plain browser talks to a server
  that is someone's node; if it can't reach it, there is nothing to show.
  This keeps the wasm client thin and ephemeral, per the charter.
- **Explicitly not building:** op queues, offline writes to remote
  content, CRDTs, client-side persistence. If deliberate offline editing
  of remote content is ever wanted, it is the existing visible-identity-
  break gesture (clone to local, edit, clone back) — not new machinery.

Why the cache lives in the node: one implementation in Go + SQLite
(machinery we already trust) serves all shells; it is unit-testable
without a browser; and it needs no charter-§7 exception — the cache is a
server-side derived fact with one owner, never a second writer of truth.
The remote stays the owner of its data; the cache only remembers answers.

The plan is four phases. Phase 0 is the foundation the rest stands on and
is being executed first.

---

## Phase 0 — transport-layer hardening (the class fix)

### The class, named

The client treats every RPC failure as a server rejection.
`clientsync.Classify` has no transport class, so call sites "reconcile"
local truth away — drop dirty buffers, discard freeze payloads, delete
pending framing — when the correct reaction to *couldn't reach* is
**keep and retry**. Ten confirmed bugs share this one mechanism (audit
2026-08-14; the archetype is `postWriteContent` destroying the only copy
of unsaved text on a wifi blip, `client/wasm/mutate.go:216-234`).

### The cure: two owners of unacknowledged state, one error taxonomy

1. **`clientsync.Outcome(err)` → `Ok | Conflict | Rejected | Transport`.**
   One classifier in the pure package. `Conflict` =
   `FailedPrecondition`; `Rejected` = the server spoke and said no
   (InvalidArgument, NotFound, PermissionDenied, Unimplemented…);
   `Transport` = the server never spoke (network error, Unavailable,
   DeadlineExceeded, Canceled). The rule every call site obeys: **local
   state may be dropped only on Ok/Conflict/Rejected — never on
   Transport.**

2. **Content: the cache's dirty entries are already the ledger.** A
   transport-failed save leaves the entry dirty; the flush sweep retries.
   Kill every path that deletes a dirty entry for any reason other than a
   server verdict (`postWriteContent`'s unconditional drop; `Apply`'s
   `TileRemoved` arm, which also fires on cross-grid *moves*).

3. **Framing/freeze: a pending-write ledger** (new pure package,
   `client/pending` — landed, and folded into `client/outbox` on
   2026-08-29 by `docs/simplify-plan.md` S5, which put content bytes in
   the same list; read the names below as that package). Today's fire-and-forget writes — well view, text
   view/scroll, root view, content zoom, url freeze state, pane layout,
   the wheel-well drift — each become an entry keyed by target id,
   latest-value-wins (framing is LWW by design). Entries clear on
   Ok/Conflict-retried/Rejected; on Transport they stay and are re-posted
   by the retry kick. This replaces the scattered "one shot, one notice"
   sites with ONE owner of "what the server hasn't acknowledged" — the §7
   cure shape.

4. **The retry kick.** One signal re-arms everything: successful SSE
   (re)connect — plus a slow timer as a backstop. The kick drains
   `client/pending`, sweeps `DirtyTileIDs`, and (fixing the silent-stale
   gap) resyncs the cache: refetch known grids, cheap-diffed by grid
   version. Boot joins the same discipline: `Handshake` retries instead
   of dying once; `gridLoadFailed`/`tileLoadFailed` become retryable
   states instead of reload-only latches.

### The bug list this closes

From the 2026-08-14 audit, in fix order: (1) `postWriteContent` dirty
drop; (2) url state/history persisted once at teardown — retry via the
ledger, plus the desktop quit path awaiting freeze results
(`registry.removeAll` currently discards them); (3) workspace layout lost
at the ascent-boundary flush — the encoded blob enters the ledger before
`Pop`; (4) wheel-well pending deleted before the RPC; (5) `TileRemoved`
deleting dirty buffers on moves; (6) the link-target flush dead-end
(resolve the target row via `GetTile` instead of erroring); (7)
`applyContentZoom` bypassing the dispatcher; (8) unload: text gets a
keepalive-fetch final flush alongside framing's beacons (bounded-size
best effort; the debounce window shrinks accordingly); (9) optimistic
framing that self-cancels when the reconcile refetch also fails —
subsumed by the ledger; (10) the four typed-input commits (rename ×3,
configure-url) retaining the typed value on Transport.

### Why was this not caught, and closing that gap

Three reasons, three closures:

- **The decisions live in the untested wasm shim.** `Outcome`,
  `client/pending`, and the drop/keep decision tables go in pure
  packages with unit tests, per the charter's extraction rule.
- **No gate ever exercises a transport failure.** Unit fakes never return
  `Unavailable`; the e2e suite never kills the server mid-session. New
  e2e spec class (`server-outage.spec.ts`): stop/restart the sidecar
  mid-edit and assert typed text survives, saves after recovery, framing
  re-posts, and the cache resyncs. This is the seam test for the whole
  class.
- **The checklist never asked.** Add to CLAUDE.md's checklist: *"For
  every RPC call site I touched: what happens to local state on
  Transport? Nothing user-typed may be dropped without a server
  verdict."*

Phase 0 exit: all ten fixed, each with a failing-first test; outage spec
green; `make check` + electron + e2e green.

---

## Phase 1 — the mount cache (read-through, in the local node)

The P3 fix: a mounted machine going dark degrades to *stale-but-readable*
instead of blank. Ships value to the desktop immediately, and is the same
layer the Flutter node uses in Phase 2.

### Where it sits

A `gridwellv1.GridwellClient` wrapper, interposed at registration for
transit plugins only (`loader.go` / `registry.go` — `Transit` is already
a config-time fact, so the wrap point is one line in `LoadAll`):

```
server → [mountcache] → sshhost plugin → tunnel → remote node
```

New package `internal/plugin/mountcache` (renamed `internal/sourcecache`
2026-08-29, docs/simplify-plan.md S7). It implements the full client
interface; non-cached methods pass through untouched. The server, the
plugins, and the wire contract do not change.

### Semantics

- **Read-through:** every successful `Info`, `GetGrid`, `GetTile`,
  `GetTilePreview`, `ReadContent` response is written to the cache DB on
  the way back to the caller. `ServeContent` is cached bounded (per-entry
  and per-mount byte caps — fs images can be huge; the caps are config
  with generous defaults).
- **Serve-stale on Transport only:** when the underlying call fails with
  a transport-class error (same taxonomy as Phase 0 — `Unavailable`,
  deadline), answer from the cache. Application errors (NotFound, the
  tombstone case) pass through verbatim — a cache must never resurrect
  what the remote says is gone.
- **Writes always pass through.** On failure the error propagates; the
  Phase 0 error surface tells the user the mount is down. The cache is
  never a write buffer.
- **Invalidation:** the wrapper taps the `Subscribe` stream it already
  forwards — `TileChanged`/`TileRemoved`/`GridChanged` update or evict
  rows as they pass. On reconnect (the existing `fanInEvents` health-up
  transition), a resync sweep refetches cached grids, diffed by grid
  version. Deletes-while-away are caught by the sweep (absence from the
  refetched grid), which is why no remote tombstone table is needed in
  this model.
- **Staleness is visible:** the existing `EventPluginHealth` sticky
  notice already says the mount is down; the client renders cache-served
  grids with that ambient notice, not per-tile chrome. (A wire-level
  `stale` bit on responses is a possible later refinement; start without
  it.)

### Storage

One SQLite DB per mount under `~/.gridwell/cache/<plugin-uuid>.db`,
using `internal/dbformat` versioning like every other plugin DB — but
**explicitly disposable**: deleting it is always safe (it re-warms), it
is excluded from `gridwell backup`, and it is NOT under the frozen-format
promise (documented in the package). Schema: qualified-id-keyed rows for
tiles/grids/info, a blob table keyed by content hash for
previews/content, byte-capped LRU eviction.

Charter check: one fact, one owner — the remote owns the truth; the
mountcache is the single owner of the *remembered answer*; the client
reads whichever the node serves. No new client state, no second writer.

### Tests

- Unit: the wrapper against a `ServeInProcess` fake remote — the existing
  test harness is exactly the right fixture. Kill the fake, assert
  serve-stale for reads, pass-through failure for writes, resync after
  revival, tombstone NotFound never masked.
- Seam: extend `make check-federation` with the missing mid-session
  partition case (noted gap: today's federation tests only cover spawn) —
  drop the tunnel, read through the cache, restore, assert resync.

### Open decisions (small) — both DECIDED 2026-08-17

- Cache-everything-touched vs. prefetching the whole mount on connect:
  **prefetch** (issue #254, landed). Every successful Subscribe — the
  initial connect and each health-up reconnect — kicks a whole-mount
  walk through the wrapper's own read methods (grids, tiles, previews,
  plugin lists, text/pane bodies, serves_page door bodies), so the walk
  doubles as the deletes-while-away resync. Caps are emergency valves
  (4096 grids, 256MB content budget per walk), not tuning knobs.
- Byte caps: as planned — generous, eviction as an emergency valve.
  ServeContent bodies are cached BOUNDED (issue #255, landed): 32MB per
  entry (oversized streams live, uncached), 512MB per mount with
  oldest-first eviction; only status-200 answers are remembered. The
  explicit "keep offline" pin stays DEFERRED by the plan's own
  condition: prefetch removed touched-only coldness, so the pin waits
  for real usage to prove the budget-bounded walk too cold.

### The supported offline-edit scenario (owner decision, 2026-08-14)

The scenario this phase must carry end-to-end: read from a remote node,
go offline (plane, camping), read everything the cache has seen — and
when editing is wanted, the gesture is **clone, edit, clone back later**.
The partial-cache problem (you clone a whole remote grid but only part
of it is cached) is decided:

- **Missing data becomes LINKS, never a hole and never a refusal.** An
  offline deep clone degrades per tile: bytes in cache → a real copy;
  not cached → a LINK naming the remote original (link creation is a
  purely local write, so it works offline; the tile ids are known —
  a visited grid's `GetGrid` response is its complete tile list). The
  dashed border already means "the content lives elsewhere," so the
  incompleteness is visible by the existing vocabulary instead of
  silent. A partial copy that LOOKS whole is the one unacceptable
  outcome; a copy that shows its seams is just honest.
- **The degrade gates on transport-unavailable ONLY.** A tile the remote
  says is GONE (NotFound, a tombstoned namespace) must never become a
  link — links are for content that exists but isn't reachable right
  now. Same Phase 0 taxonomy, applied server-side.
- **Back online, it all resolves.** The links point at the originals and
  work again the moment the mount heals; right-dragging one completes
  the copy with the same gesture. **Links clone as links, both
  directions**: cloning the edited grid back to the remote turns each
  link into a same-node reference to the original it always named —
  never a materialized copy of content the user never touched.
- **Clone-back is a NEW subtree, not a merge.** There is no cross-plugin
  move and no sync-back; reconciling the edited copy with the original
  stays a set of explicit, visible gestures. Copies are new identities.
- **Prerequisite: deep cross-plugin copy.** Today a solid well is
  refused across a plugin boundary ("until deep cross-plugin copy
  exists"); this scenario IS that feature, with the per-tile
  degrade-to-link rule riding on top. It lands in this phase, tested
  against a fake remote that goes dark mid-walk.
- **Pinning becomes an optimization, not a prerequisite**: a "keep
  offline" prefetch guarantees a FULL copy is possible; without it the
  clone still succeeds, with links marking what wasn't there.

---

## Phase 2 — the Flutter node (every device is a node)

### The spike first (do before committing the phase)

`gomobile bind` a minimal package that starts a gridwell node and returns
its loopback address. Proves: modernc.org/sqlite under gomobile (pure Go
— expected clean), binary size, iOS lifecycle. Timebox it; the rest of
the phase waits on its result.

### In-process plugins (owner decision required)

iOS forbids fork/exec, so the go-plugin subprocess model cannot run
there. `plugin.ServeInProcess` (`internal/plugin/loader.go:78`) already
serves the identical gRPC surface over loopback with no subprocess — it
is "test-only" by policy, not by capability. The decision to make:
**promote the in-process path to a supported production mode on mobile**
(a `PluginConfig` flag or a mobile build default). What subprocess
isolation actually buys today is crash isolation and independent binary
upgrade — neither load-bearing for correctness; the wire contract and id
discipline are identical on both paths. Desktop keeps subprocesses.

### The embedded node

- New `mobile/` bind package: `Start(homeDir string) (addr string, err)`,
  `Stop()`. Internally: ensure config (auto-`init --kind localdb --name
  home` on first run, mirroring the desktop sidecar's heal), load plugins
  in-process, serve on `127.0.0.1:0`.
- Forced on mobile: `disable_shells` (no PTY), no password (loopback,
  app-sandboxed home dir), embedded web client (already in the binary).
- Flutter changes: boot calls `Start`, the host webview loads the
  loopback origin. The server-URL screen becomes unnecessary for boot;
  reaching OTHER nodes becomes what it is everywhere else — a mount.
- Lifecycle: `Stop` on app termination; SQLite WAL already survives
  abrupt suspends. No background sync — the cache refreshes while
  foregrounded, which is the honest contract.

### Reaching the home node from the phone (decision)

The phone now mounts the home node instead of browsing it. Two routes:

1. **ssh mount** — works today, but means ssh keys on the phone.
2. **Direct-dial connection** — a connection-well variant that dials a
   node export address directly (tailnet-secured, no ssh tunnel). The
   node export is already designed for VPN-only exposure; `sshdial` is
   already the only ssh-specific piece. Modest addition to the connection
   params (`transport: direct`, addr), same tombstone/instance
   semantics.

Route 2 is the phone-friendly shape; it is also useful on desktops inside
a tailnet. Decide when Phase 2 starts.

### What the phone gets

Its own durable localdb (notes on the plane, no network involved), plus
the home machine's grids readable-stale through the Phase 1 cache
whenever the tailnet is down. Live url tiles on the phone remain
capability-gated exactly as today.

---

## Phase 3 — offline UX polish

Deliberately last; Phases 0–2 make the states *correct*, this makes them
*legible*. Scope:

- Uncached/unreachable tile faces: cached preview + ambient staleness
  notice (exists via plugin health); label-text fallback stays for
  never-cached. `grid <id> unavailable` becomes retryable in place
  (Phase 0 already makes the latch retry; this is the affordance).
- Soft refusal on descent into uncached remote content: error strip, pane
  stays put — never a blank room.
- A "keep offline" pin gesture on wells/subtrees (prefetch + retention
  hint for the mountcache) — optional, only if touched-only caching
  proves too cold in practice.

---

## Sequencing and gates

| Phase | Depends on | Gates | Status |
|---|---|---|---|
| 0 hardening | — | check + electron + e2e (new outage specs) | **LANDED** 2026-08-15 (`938d4f2..c4213cf`) |
| 1 mountcache | 0 (taxonomy) | check (unit vs fake remote) + federation (partition spec) | **LANDED** 2026-08-15 (`2d3def3`; deep-copy degrade `98f266a`) |
| 2 flutter node | spike; 1 for cache value | check + mobile build in CI; real-hardware pass | Go half + Dart half **LANDED** (`9fa9510`, mobile-app boot commit); REMAINING: gomobile packaging (AAR/xcframework), the Kotlin/Swift `gridwell/node` channel shim (contract in `apps/mobile/lib/node.dart`), and the real-hardware pass |
| 3 UX | 0–2 | e2e + web | **LANDED** 2026-08-17: the wire-level `Grid.stale` bit (stamped only by the mountcache's serve-stale path) surfaces as the bar's quiet "offline" chip — bar chrome only, tiles never move or restyle; pinned by the federation partition gate (the bit crosses the real chain) and a web spec (a dark mount re-enters as a marked memory, tiles intact). The pin gesture stays deferred pending real usage |

Each phase lands and ships alone. Also landed along the way: the
mid-session partition federation gate (`test/federation/partition_test.go`)
and the offline deep-copy degrade end to end over real binaries.
