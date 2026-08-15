# Offline

A deep-dive on what offline operation would require, written against the
code as of 2026-08-14. This is an options document, not a plan: it maps
where the system actually stands, lays out the canonical offline models
and how each fits, and isolates the sync/conflict problem — the hard
part — with the owner decisions that would have to be made. Nothing here
is decided.

---

## 1. What "offline" means here — three different partitions

Offline is not one problem in this architecture; it is three, with very
different costs:

**P1 — desktop app, no internet.** The Electron shell bundles the server
and every plugin as a sidecar child process and talks to it over loopback
(`apps/desktop/src/main/sidecar.ts:60`, `lines.ts:55`). The desktop app is
structurally never offline from its own data. Losing the internet costs it
exactly what it costs the server: live url tiles can't browse, and
federation mounts go dark. **This case mostly already works.** Two caveats:
a `bind:` pinned to a Tailscale IP makes the window's own origin
non-loopback (`lines.ts:66-69` only rewrites wildcard hosts), so a tailnet
outage can partition the desktop from its own sidecar; and a sidecar crash
leaves a window whose every RPC fails, framed as "restart the app"
(`index.ts:158-177`).

**P2 — a client (phone/browser) partitioned from its server.** The Flutter
shell has no sidecar; it is a webview over the server's own web client
(`apps/mobile/lib/main.dart:221`). With the server unreachable, *nothing*
loads: there is no service worker, no cached app shell, the wasm comes from
the server, and auth is a live check against the server's config-derived
token (`internal/server/auth.go:48`). Today's offline UX is a snackbar over
a blank screen (`main.dart:291-297`). **This is the partition the request
is about**, and it is the expensive one.

**P3 — the server partitioned from its mounts.** Already handled by a
coherent doctrine — "unreachable is not gone" — enforced independently in
four places (fs/proc sweep rules / I12, `nodegrid.go:170-179`,
`sshhost.go:300-312`, `registry.go:92` Transit-as-config-fact). Nothing is
ever swept because it went quiet. But the degraded form is *blank*, not
*stale-but-readable*: a remote tile's frozen preview blob lives on the
remote (`preview_blob_id` resolves at the serving node), so it is exactly
as unreachable as the live content. After a reload, an unreachable mount
renders as `grid <id> unavailable` and label-text placeholders.

A useful framing: P1 is done, P3 needs a *host-side cache of remote faces*,
and P2 needs everything in this document.

### 1.1 Checking the premise

"We write through a cache, so we can operate without a connection" — the
cache exists, but the write path is mostly **RPC-first, refetch-after**,
not cache-first. `doTileMutate` posts the RPC and then `fetchGrid` resyncs
(`client/wasm/mutate.go:281-298`); the cache is not touched before the
call. Only two families are genuinely optimistic today:

- **Framing** — `persistWellView` patches the cache and pushes a synthetic
  `EventTileChanged` before the RPC (`input.go:1996-2006`), then
  `postFramingPersist` with one conflict retry (`mutate.go:147-164`).
- **Text keystrokes** — every keystroke mirrors into the content entry
  (`PutEditedContent`), debounced 600 ms, flushed through the save queue.

Everything else — create, place, clone, delete, rename, adopt — blocks on
the server and refetches. So offline writes are not "already almost
working"; the dominant mutation path has no local arm at all. The cache is
also purely in-memory: a repo-wide search finds no localStorage, no
IndexedDB, no OPFS, no service worker. A reload is a cold boot from the
URL.

---

## 2. Where the code stands today

Facts an offline design builds on (or fights):

**Concurrency model.** Content mutations are compare-and-set on a per-tile
`version` counter (`checkTileVersion`, `internal/store/tiles.go:25-35` →
`ErrVersionConflict` → Connect `FailedPrecondition`). Framing writes carry
the same version *precondition* but never advance the counter
(`emitTileChanged` vs `finishContentEdit`, `tiles.go:59-85`) — framing is
effectively last-writer-wins under a stale-read guard. The version is
paired with bytes at the owner (`ReadContent` returns data+version in one
call) — the save-basis contract that makes stale saves 409 instead of
stomping.

**No history, no tombstones.** State is purely current-value: no oplog, no
audit trail, no soft delete. `DeleteTile` hard-deletes the row
(`tiles.go:503-527`); the only durable trace is the id gap plus a bumped
grid version. A client that was away can discover a delete *only* by
refetching the grid and noticing the absence. (Tombstones could be added
additively — a new table is within the frozen-format contract,
`internal/store/CLAUDE.md`.)

**No resumable event stream.** `SubscribeRequest` is empty — no cursor, no
sequence number (`api/gridwell/v1/data.proto:610`). The server coalesces
undelivered events per entity and discards them at cancel
(`internal/server/events.go`). On reconnect the client refetches
**nothing** (`startSSE`, `client/wasm/main.go:1095-1145`): it silently
renders stale state with the disconnect notice cleared. This is a
correctness gap even *before* offline is a feature.

**No transport/application error distinction.** `isVersionConflict` checks
`FailedPrecondition` and everything else — a dead network, a 502, an
`InvalidArgument` — takes the same "surface and forget" arm
(`mutate.go:48-96`). Worse: `postWriteContent` reacts to *any* failure by
dropping the dirty buffer (`mutate.go:216-247`), which orphans the user's
typed-but-unsaved text after a transient blip (the DOM still shows it, but
it will never flush again — until the next keystroke recreates the entry
at base 0, guaranteeing a 409). **This is an edit-loss bug today,
independent of any offline work**, and worth fixing on its own charter
terms (the drop conflates "server refused" with "couldn't reach the
server").

**Ids are server-minted.** Tile/grid/blob ids are per-plugin SQLite
AUTOINCREMENT, never reused. A client cannot create a tile without the
server assigning the id — so offline creates need either provisional ids
with remap-on-sync, or a (frozen-format-hostile) move to client-minted
ids. Stored references (`link_target_id`, `child_grid_id`) key on these
ids, so a provisional id must never leak into a stored reference before
remap. Separately, `object_id` — a 128-bit random provenance marker
carried across clones and cross-plugin copies (`uuid.go:16-24`) — already
exists and is the one identity that survives a node boundary; it is a
natural hook for sync correlation.

**Blobs are content-addressed but the address stays home.** `blobs.hash`
is sha256, unique, deduped (`clone.go:273-292`) — the perfect validator
for an offline blob cache — but it is not on the wire anywhere; clients
see only `blob_id`/`preview_blob_id`. There are no ETags, no
Last-Modified, no cache headers in the entire tree (the only
cache-control directive in the codebase is `no-store` on the login page).
`created_at`/`updated_at` exist as columns but are storage-only.

**Auth is a liveness check.** The cookie token is derived from the current
password and verified against it per request; there is no local credential
store and nothing signed. A partitioned client cannot authenticate against
anything — and per charter §7 there is deliberately no client-side data to
authenticate *to*. Any offline mode implicitly creates the first
client-held copy of user data, which changes the threat model on a stolen
phone (see §8).

**Boot is one non-retried RPC.** `bootstrap` calls `ListPlugins` exactly
once; failure yields a permanently empty shell until reload
(`main.go:763-791`).

---

## 3. Groundwork every option needs (Tier 0)

These are prerequisites for *any* offline model — and each is defensible as
pure robustness work with no offline commitment:

1. **An error taxonomy.** Classify transport-unreachable apart from
   application rejection, in one place (`clientsync` is the natural home).
   Retryable vs. fatal is the axis every later decision hangs on.
2. **Stop destroying dirty buffers on transport failure**
   (`mutate.go:216-247`). A dirty entry survives until the *server*
   rejects it. This alone converts a wifi blip from an edit-loss event
   into a delayed save.
3. **Resync on reconnect.** Either a cursor on Subscribe (wire change) or
   a client-side sweep on stream re-establishment. The cheap sweep exists
   already in embryo: `grids.version` bumps on every structural change, so
   a "grid id → version" summary RPC lets a reconnecting client diff its
   cache in one round trip and refetch only what moved. This also closes
   today's silent-staleness gap.
4. **Boot and fetch retry.** `bootstrap` and `fetchGrid` failures need a
   timed retry, not a dead shell (`tileLoadFailed` is currently "cleared
   only by a reload").
5. **App-shell availability (P2 only).** The client must *load* without
   the server: a service worker caching `index.html` + wasm + vendor for
   browsers, and/or the Flutter app bundling the web client from its own
   assets (it ships from the same repo — only data need cross the
   network). Version skew between a cached client and a newer server
   becomes a real compatibility surface the moment this exists.

None of this decides the offline model. All of it is needed by every one
of them.

---

## 4. Canonical models and how each fits

The lens for "fit" is the charter: things stay as you left them; nothing
changes except by explicit action; one fact, one owner; identity breaks
only where the user can see them. Single primary user; conflicts are
almost always *self*-conflicts (you on the phone vs. you at the desk).

### Model A — CRDT replica (Automerge / Yjs style)

Every device holds a full replica; all edits merge automatically and
deterministically; no conflicts by construction.

*Fit: poor.* Three independent disqualifiers. (1) It rewrites the storage
format around CRDT metadata — the format is frozen and additive-only.
(2) Automatic merge is the *opposite* of the guiding rule: a text tile
that silently interleaves two devices' edits is precisely "something
changed that you didn't change here." (3) The concurrency the model pays
for — many writers, character-level merge — is not this product's shape.
CRDTs answer a question Gridwell doesn't ask.

### Model B — durable op-log with server rebase (Linear/Figma sync-engine style)

The client persists a snapshot cache plus an ordered queue of mutations.
Offline, ops apply to the local snapshot and queue. On reconnect, ops
replay against the server in order, each carrying its version claim; the
server stays the single arbiter; rejected ops surface for resolution.

*Fit: strong, and notably cheap here.* The wire contract is already
op-shaped — every mutation is one RPC with a version claim, and the CAS
machinery is the rebase conflict detector, unmodified. The op classes
already sort themselves (§5). The server, store, and plugins need almost
nothing: sync is a client concern replaying the existing 17-RPC surface.
Costs: a durable client store (IndexedDB/OPFS under wasm — a charter §7
owner decision, see §8); provisional ids for creates with remap before any
op that references them; tombstones or a resync sweep to learn about
deletes; and the conflict UX (§5.3). This is the model the existing
architecture is *accidentally closest to*.

### Model C — revision-tree replication (CouchDB/PouchDB style)

Full bidirectional replication with per-document revision trees; conflicts
are stored as siblings; a deterministic winner is served until the user
resolves.

*Fit: middling.* The conflict *presentation* (both versions preserved,
explicit resolution) is charter-aligned. But the machinery — rev trees,
replication checkpoints, per-doc history — has to be built into a store
whose schema is frozen and which deliberately keeps no history. It is
model B's conflict handling with a much heavier persistence bill, and its
strength (any-topology multi-master replication) buys nothing in a
star-shaped single-user system.

### Model D — file-sync LWW with conflict copies (Dropbox/Obsidian/Syncthing style)

Whole-object last-writer-wins; on concurrent edit, keep both — the loser
becomes a visibly-marked "conflicted copy."

*Fit: mixed, instructive.* The resolution *gesture* is deeply aligned:
nothing is lost, nothing silently merges, the identity break is visible —
it rhymes with clone-then-delete for cross-plugin moves. But pure LWW
needs wall-clock ordering (nothing here has trustworthy clocks and nothing
should — the store deliberately has no wire-visible timestamps), and a
minted conflict-copy tile is itself a placement the user didn't make (the
grid changed shape by itself). As a *resolution option inside model B*
("keep both" → an explicit clone) it works; as the whole model it is
weaker than the CAS machinery already in hand.

### Model E — every device is a node (the federation-native model)

Reframe offline entirely: the phone runs its own gridwell node (Go builds
for iOS/Android via gomobile; the server is already one self-contained
binary) with its own localdb. Your phone's tiles live on the phone; the
home machine's live at home; links cross. "Offline" is then just P3 —
a mount being down — which the system already survives by design. No
replica of anyone else's data ever exists, so there are no write conflicts
*at all*: one fact, one owner, at machine granularity.

*Fit: philosophically the purest — and the most honest about its limits.*
It requires zero new sync machinery and no charter exception. What it
cannot do is the thing most likely wanted: reading and editing *home*
content from the plane. Every cross-node access is exactly as available as
the mount. It also composes with everything else: model E plus a P3
remote-face cache gives stale-readable remote grids; model E plus explicit
check-out/check-in (clone to local node → edit → clone back, the visible
identity break the charter already prescribes for cross-plugin moves)
gives deliberate offline editing without any hidden replica. Whether that
explicitness is a feature or a chore is a product judgment.

### Model F — read-only offline (persistent cache, no offline writes)

Persist the read side — grids, tiles, content bodies, previews — in
durable client storage, validated by grid versions on reconnect. Offline,
everything cached renders read-only; writes are refused visibly (or
framing writes alone are queued, being idempotent-with-retry already).

*Fit: strong as a floor.* No conflicts exist (or only framing LWW ones,
which today's semantics already tolerate). The UX question shrinks to "how
does an uncached tile look" (§6). Delivers the majority use case —
consulting your own notes/grids away from connectivity — for a small
fraction of model B's cost, and every artifact (durable store, cache
validation, resync sweep) is a strict prerequisite of B anyway. Its
charter exposure is one decision: a client-held *copy* of server facts
(read-only, never a second writer) still needs the §7 owner decision.

### Sequencing note (not a prescription)

The models are not all mutually exclusive. F is a strict subset of B; E is
orthogonal to both and composes with either; A and C are alternatives to B
that replace its conflict story rather than extend it. A staged path
F → B exists if wanted; E exists as a way to change the question instead.

---

## 5. Sync and conflicts — the hard part

Assume some form of offline writes (model B, or D's semantics inside B).
The star topology does most of the heavy lifting: every sync is
client ↔ one authoritative server, so the per-tile version counter is a
sufficient causality detector — no vector clocks, no wall clocks, no
distributed ordering. (This stops being true the moment two *nodes* sync
peer-to-peer under model E extensions; that variant should be scoped out
of a first design.)

### 5.1 Conflict taxonomy by op class

The existing RPC surface sorts cleanly:

| Op class | Ops | Offline replay behavior | Conflict character |
|---|---|---|---|
| Framing | `SetWellView`, `SetTextView`, `content_zoom`, `url_frozen`, `SetRootView`, pane-layout `WriteContent` | Idempotent overwrites; last replay wins | Benign — two devices disagreeing about a viewport has a trivial resolution (latest engagement wins), matching today's guarded-LWW semantics. One wrinkle: framing ops *carry* a version claim, so replay after a remote content edit 409s; the existing one-shot re-claim retry (`postFramingPersist`) already handles exactly this shape |
| Content | text `WriteContent`, url address, rename | CAS on version; concurrent edit → 409 | The real conflicts. Rare (self-conflict only) but must be handled visibly (§5.3) |
| Create | `CreateTile` (+ the body write that follows) | No precondition; replay is safe against lost-ack duplication *only* by the overlap refusal at the same (x,y) | Needs provisional ids + an idempotency key (a client-minted `object_id` per create would make replays exactly-once — the field already exists and is already client-visible provenance) |
| Structural | `PlaceTile`, `CloneTile`, `DeleteTile` | All version-claimed; replay after remote change → 409 | Move-vs-edit and delete-vs-edit races surface as conflicts, which is correct; the asymmetric case is edit-vs-*remote-delete* — replay gets NotFound, and with no tombstones the client cannot distinguish "deleted while I was away" from data loss |

Two properties of the store actively help: byte-identical content writes
are true no-ops (no bump, no event — `move_clone.go:95-105`), so redundant
replays self-absorb; and ids are never reused, so a stale reference can
dangle but can never resolve to the *wrong* thing.

### 5.2 What the server side needs (small, additive)

- **Tombstones** (new table, additive-only compatible): lets a replayed
  edit against a deleted tile fail as "deleted at T by you-elsewhere"
  rather than a bare NotFound, and lets the resync sweep report deletions
  without diffing full grid fetches.
- **Create idempotency**: accept a client-supplied `object_id` (or a
  dedicated idempotency key) on `CreateTile` and return the existing tile
  on replay. Turns lost-ack duplication from "second tile appears" into a
  no-op.
- **A cheap resync summary**: grid-id → grid-version (and possibly
  tile-id → tile-version per grid). Everything else — CAS, no-op writes,
  error classes — already exists.

The plugins and the wire contract otherwise stay untouched; sync
machinery concentrates in the client, which is where the charter wants
untestable orchestration *not* to be — so the queue/replay/remap logic
belongs in a pure `client/*` package (`client/oplog` or similar) with the
wasm shim as a thin adapter, per the §5 extraction rule.

### 5.3 Resolution UX — the options

For the content-class 409s, the candidate policies, all conflict-aware
(silent LWW is excluded by the charter):

1. **Surface and choose.** The server version stands; the losing offline
   edit is presented — keep mine / keep theirs / keep both. "Keep both"
   mints an explicit clone (D's gesture, made deliberate). Strongest
   charter fit: nothing changes without an explicit action, in either
   direction. Cost: an interruption at sync time, and a new modal surface
   to design.
2. **Newest-engagement wins, loser parked.** The offline edit lands, the
   displaced version is retained (a version-history breadcrumb or an
   auto-clone in a designated "conflicts" well). Lower friction, but both
   halves violate the letter of the rule: the tile changed under whoever
   made the older edit, and a parked copy is a placement nobody made.
3. **Refuse and retain.** The offline edit simply fails and stays local
   (dirty, flagged) until the user reconciles by hand — the current 409
   behavior extended with durability. Simplest honest option; risks
   accumulating stale local edits the user forgets.

Since conflicts here are self-conflicts, frequency will be very low —
which argues for the option that is *clearest when it finally happens*
over the one that is smoothest in aggregate.

### 5.4 Edge cases the design must answer

- **Two devices offline at once.** Star topology handles it — each syncs
  independently and the second one's conflicts surface normally — but the
  conflict UX must tolerate *batches*, not just single 409s.
- **Provisional-id leakage.** An offline create followed by an offline
  link to it: the link op references a temp id that must remap before
  replay. The remap table is itself durable client state that must survive
  a crash mid-sync (replay must be resumable and idempotent).
- **Placement collisions.** Two offline creates (or one create vs. a
  remote move) landing on the same cells → `ErrOverlap`. Auto-nudge
  violates "placement is persistent"; surfacing it as a park-and-ask is
  the charter-consistent shape.
- **Blob availability.** Cloning or descending a tile whose blob was never
  cached; conversely, an offline-written blob (image paste, preview) that
  must upload before the op referencing it replays. Op replay has a
  dependency order: blobs → creates → references → framing.
- **Password change while away.** Every queued op replays into 401. The
  queue must survive re-auth, and the local cache's own protection story
  matters (§8).
- **Client/server version skew.** A cached app shell (Tier 0.5) or a
  long-offline op queue meets an upgraded server. The wire contract is
  additive-only, which helps; the op queue format needs the same
  discipline.
- **Tombstoned ssh namespaces.** A queued op addressing
  `<ssh>/<conn>/...` whose connection was tombstoned meanwhile: NotFound
  forever, must fail queue-fatally with a clear surface, not retry.
- **fs/proc writes don't exist** (read-only plugins) but their *content*
  in an offline cache is a stale projection of a live machine — fine by
  the same sweep-rule logic (I12), but the staleness should be visible
  (§6).
- **Live tiles.** url/shell liveness is a capability, not data; offline
  they are frozen, full stop — the frozen preview is the already-designed
  degraded form. The only requirement is that the preview bytes be in the
  offline cache, which they are not today (previews are RAM-only,
  `client/preview`).
- **Clock discipline.** Nothing in the design above needs a wall clock.
  Keep it that way; any policy that reaches for timestamps (D-style LWW)
  imports clock skew as a correctness input.
- **Storage pressure.** localdb is capped at 16 MiB per blob and is
  single-tenant — a full mirror of a personal DB is likely tens to low
  hundreds of MB, comfortably inside IndexedDB/OPFS quotas; but fs-plugin
  content (photos through the /content/ door) is unbounded and needs
  either exclusion or an explicit pin gesture (§6).

---

## 6. UX when something is not cached

Whatever the model, some tile will eventually not be openable offline. The
existing vocabulary nearly covers it:

- **Grid-level**: `grid <id> unavailable` already renders as a muted
  status line (`render.go:574`). Offline needs the same state to be
  *first-class and retryable* (today `tileLoadFailed` clears only on
  reload) and to distinguish "unreachable" from "not found."
- **Tile-level**: the frozen preview is the natural offline face — it is
  already the designed answer to "what does this look like from outside."
  A cached preview + a subtle offline badge reads as "the room is there,
  the door is closed." No cached preview degrades to the label-text
  fallback that already exists.
- **Descent into uncached content** should refuse softly at the gesture
  (the same way a plain browser descends a url tile frozen and silent) —
  an error strip entry, pane stays put. Never a blank room: a blank grid
  is indistinguishable from an empty one, which lies about the user's
  space.
- **Staleness honesty**: a persistent-cache client rendering old data
  should say so once, ambiently (the errsurface already has sticky
  per-source notices), not per-tile. fs/proc projections doubly so.
- **What gets cached** is its own product decision: everything-touched
  (transparent, but unbounded and surprising), whole-localdb mirror
  (bounded, complete for the common case, ignores fs/mounts), or an
  explicit **pin gesture** ("keep offline" on a well/subtree — the most
  charter-shaped answer: presence in the offline cache is a fact the user
  placed). These compose; they are not exclusive.

---

## 7. What each option costs, roughly

| | New client machinery | New server machinery | Conflict surface | Charter friction |
|---|---|---|---|---|
| Tier 0 (robustness) | error taxonomy, retry, resync sweep | version-summary RPC (optional) | none | none — it's bug-fixing |
| F: read-only offline | durable cache + validation, offline UX | none (summary RPC helps) | none | §7 exception for a read replica |
| B: op-log + rebase | F + durable queue, provisional ids, replay engine, conflict UX | tombstones, create idempotency | §5.3, batched | §7 exception for queued writes; conflict-UX decisions |
| D (as B's "keep both") | B + conflict-clone flow | same as B | simplest to explain | minted placements |
| C: rev trees | replication protocol client | rev storage in a frozen schema | stored siblings | heavy format pressure |
| A: CRDT | new data layer | new data layer | none (by fiat) | fundamental — auto-merge vs. the rule |
| E: device-as-node | none (gomobile packaging) | none | none | none — but doesn't solve remote-content-offline |

---

## 8. Owner decisions required before any build

1. **The §7 exception.** Any offline cache is client-held user data —
   the first ever. Read-only copy, or writable queue, or neither, is a
   charter amendment, not an implementation detail. (Precedent exists:
   Chromium's session partition is already a decided host-local
   exception.)
2. **The at-rest story for that cache.** Today a lost phone leaks a
   cookie; with an offline cache it leaks the data. Encrypt-at-rest with
   what key, or accept-and-document, is a security posture decision.
3. **Which partition to actually solve.** P3 (cache remote faces
   host-side) and P1's bind edge are cheap and self-contained; P2 read-only
   (F) is medium; P2 writable (B) is the full program. These are separable
   commitments.
4. **The conflict-resolution policy** (§5.3, options 1–3) — a product
   decision that shapes the store additions (tombstones, idempotency keys)
   and should be made before, not after, the queue exists.
5. **The cache-scope gesture** (§6): implicit-everything, whole-DB mirror,
   or explicit pinning.
6. **Whether model E is on the table** — it changes what "offline" means
   for the mobile app entirely, and its packaging question (a Go node on
   iOS) is worth a spike before committing either way.

---

## Appendix: today's gaps that offline work would inherit

Compiled from the code audit; each is worth fixing regardless of the model
chosen. (1) Transport failures drop dirty text buffers
(`mutate.go:216-247`). (2) SSE reconnect never resyncs — silent staleness
with a cleared notice (`main.go:1095-1145`). (3) Boot `ListPlugins` is
one-shot; failure is a dead shell (`main.go:763-791`). (4) `tileLoadFailed`
clears only on reload (`main.go:203`). (5) No transport/application error
taxonomy (`mutate.go:48-57`). (6) Frozen previews of mounted tiles have no
host-side cache — a dark mount blanks them on reload (P3). (7) The unload
beacon path covers framing only; a content save racing a tab close is lost
(`internal/rpc/beacon.go:10-14` documents this).
