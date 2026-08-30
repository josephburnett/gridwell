# Architecture review — 2026-08-29

A fresh, whole-system read of the non-test code (~2 MB, every file), taken
after the one-node fold (`docs/one-node.md`). Written against the owner's
brief, not against the existing docs or comments:

> A few primitives (text, shell, web, grid). Minimal and uncomplicated.
> Data stable, self-describing, one sqlite file. Adaptable in content,
> not in experience. As close to the physical world as possible, except
> where computers exceed it (the well, the infinite plane). Things stay as
> I leave them. Stability is the value; the data format first, the
> application second. Simple, in the Hickey sense.

This is an assessment and a direction, not a plan. Each finding names
the mechanism and the file so it can be verified, disputed, or acted on.

## What holds together

- **The id codec** (`api/rpc/types.go`: `QualifyID`, `SplitID`,
  `NamespaceOf`; `internal/server/server.go`: `resolve`). One segment per
  hop; routing by the shape of the first segment (digits = home, letters
  = connection, else plugin). Federation is string concatenation. This is
  the best idea in the system and the thing to protect.
- **The store's format discipline** (`internal/local/store/schema.go`,
  `migrations.go`, `internal/dbformat`): a frozen v1, an additive chain,
  the equivalence test, WAL pinned on every open. This is the "data lives
  forever" promise actually enforced by machinery.
- **Decisions in pure packages** — `client/dragdrop` (one drop verdict for
  preview and commit), `client/clientsync` (one error classifier, one
  reaction table per mutation family), `client/errsurface`,
  `client/zoomtrans`, `client/pane`. The pattern is right: decisions are
  tested, pixels are glue.
- **Events**: `internal/eventhub` — one coalescing fan-out, shared by the
  store and the transport.

## Where it is complected

> Every finding below was acted on and is now resolved; what landed,
> and where a workstream deliberately did otherwise, is the
> **Resolution** section at the end of this document. The findings
> text stands as written, as the record of what was true.

### 1. "Where am I" has five representations

A pane's place is spread across:

| Representation | Where | Owns |
|---|---|---|
| `pane.Path` | `client/pane/pane.go` | wells descended within one namespace |
| `pane.Up` frames | same | namespace crossings (portal descents) |
| the ascent stack | `client/panestate` | the viewport saved before each descent |
| `workspace.Stack` | `client/workspace` | pane-tile crossings (whole-tree swaps) |
| the URL | `client/url` | a fifth encoding of the same place |

Each has its own descent and its own ascent. In `client/wasm` there are
eight ascents — `startAscent`, `instantAscend`, `ascendOneLevelInstant`,
`exitTextInstant`, `ascendPortal`, `animatePortalAscent`,
`ascendWorkspaceLevels`, `ascendToChainCrumb` — and the code's own
comments say each "mirrors its animated twin's writebacks". Physically
there is one gesture: go back through the doorway you came in by. The
data model's *ownership* boundaries (a local well, a link into another
namespace, a pane tile) leaked into the *experience* mechanics. This is
the single largest source of glitch risk, and it lives in the untested
shim (`client/wasm/input.go`, `workspace.go`, `bottombar.go`).

### 2. Framing — the core invariant — is stored three ways

"How this grid looked when I left it" is:

- a well's `tiles.view_x / view_y / view_zoom` — integer origin plus an
  intrinsic ratio (`zoomtrans.IntrinsicFromLive`);
- home's root: `system.root_view_cx / cy / zoom` — float center;
- a plugin context's root: `grids.root_cx / cy / zoom` — float.

Three writers (`SetWellView`, `SetRootView` on the home store,
`Namespace.SetRootView`), two shapes. `zoomtrans.ViewOriginFromCenter`
and the intrinsic-ratio math exist to make the integer quantization
idempotent across round trips. One representation — float center and a
pane-size-independent zoom — deletes that math and two of the writers.

### 3. `version` is doing two jobs

It is the optimistic-concurrency claim for user content **and** the
cache-staleness signal (`client/cache` `reconcileContent` compares it to
the save basis), and it bumps on automatic captures: a page title, a
preview jpeg, a shell's foreground command (`store/url.go`,
`store/shell.go`, `finishContentEdit`). So framing writes — which are
last-writer-wins by design — claim versions, race the automatic bumps,
and need `claimOnce`, `doFreezeWrite`, `postFramingPersist` and the
pending ledger (`client/wasm/mutate.go`, `client/pending`). If only
content bytes were versioned and framing carried no claim, most of that
dispatch layer has no reason to exist.

### 4. Three copies of Tile

RESOLVED 2026-08-29 (`docs/simplify-plan.md` S6). The proto is the owner:
`api/rpc`'s records and conversions are GENERATED from it
(`api/rpc/internal/gen`, run by `buf generate`, staleness caught by
`make proto-check`), the store's five column lists are one descriptor
(`internal/local/store/columns.go`), and each derived wire field has a
named owner (`ARCHITECTURE.md` §7.1). `conv.go`'s mirror half and both
drift lints are gone — there is nothing left to drift.

Was: `pb.Tile` ↔ `rpc.Tile` (hand-mirrored in `api/rpc/conv.go`, pinned
by a drift lint) ↔ the store's scan/insert column lists. Ten of the wire
fields are derived and never stored. The charter says "derive once"; this
is copy-and-lint.

### 5. Home crosses a gRPC hop to reach itself

RESOLVED 2026-08-29 (`docs/simplify-plan.md` S2): namespaces are Go values
(`internal/namespace`); gRPC survives only at the plugin subprocess and the
federation socket, and `api/compose/compose.go` — the bufconn loopback — is
deleted.

Browser → Connect → `connectHandler.route` → bufconn gRPC
(`api/compose/compose.go`) → `internal/local.Plugin` → store. The
uniformity ("everything is a `GridwellClient`") bought the router one
shape at the price of serializing the user's own data three times per
edit. The registry's transport slot is documented as transitional
(`internal/plugin/registry.go`). Namespaces should be Go values
in-process; gRPC belongs only at the federation socket.

### 6. Two remembering systems

Plugin memory — the `listings` table, the union-with-remembered rule in
`internal/pluginhost/adapter.go` — and `internal/plugin/mountcache` (wire
protos in `cache.db`, a prefetch walker, a bounded body cache). Same
concern: what did the source last say. Two engines, because a connection
is not a plugin.

### 7. The forever file carries disposable and dead things

- `listings` is a cache living under the frozen-schema promise ("durable
  in practice, disposable in principle" — `store/external.go`).
- The `session` table: the feature died 2026-07-26; the DDL is still
  created and verified on every open (`store/schema.go` `sessionDDL`,
  `schema_check.go`).
- `configure_plugin_id`, `CreatePluginWell`, `AdoptChildGrid`: the
  instance picker retired 2026-08-23; the column, the CHECK branch, the
  store verbs and the client arm remain.
- `create_schemas`: no plugin declares one, no client reads one (the
  handler's own comment).
- Menu-entry creation (`#258`): declared by fs, stripped by the adapter,
  yet `client/schemaform`, `client/wasm/entry_params.go` and
  `modal_form.go` ship the form.
- `object_id`: written on every row and carried through every clone and
  link; no reader was found that decides anything on it.

### 8. Shells break "the same everywhere"

PTY bytes ride a second client stack — Electron main → gRPC → the
federation socket (`apps/desktop/src/main/shellgrpc.ts`,
`shellstreams.ts`) — while every other primitive rides Connect from the
page. The consequence is that the phone has no shell. The primitive set
is supposed to be closed and uniform across hosts; a WebSocket on the web
door is the one place this review would *add* a transport.

## The shape to aim for

1. **One place model.** A pane is a stack of frames
   `(gridID, doorTileID, viewport)`. Every descent pushes — a well, a
   link, a text/url/shell tile (a frame with no grid), a pane tile — and
   every ascent pops. One framing writeback, fired on pop and on settle.
   `Up`, the ascent stack, and most of `workspace.go` go away.
2. **One framing representation**, float, stored on the doorway; a root
   with no doorway keeps a row of the same shape.
3. **Versions on content bytes only.** Framing is unversioned LWW. One
   outbox for offline, not a ledger per write family.
4. **Namespaces as Go interfaces** in-process; gRPC only at the export.
5. **Prune the durable schema**: drop `session`, `configure_plugin_id`,
   `object_id`; move `listings` out of the forever file, or declare it
   durable and mean it.
6. **One transport**, shells included, so the primitive set is the same
   on every host.

The fundamentals really are small — grid, tile, doorway, framing, the id
chain. The complexity is accumulated *mechanics* around navigation and
persistence, not the model. That is good news: it is deletable without
changing anything the user sees.

---

# Resolution — 2026-08-29

Every finding above was worked as a numbered workstream of
`docs/simplify-plan.md` and landed on `main` in `10c009a..7cf468f`. The
findings text is left as written, as the record of what was true that
morning; this section says what is true now, what mechanism holds it, and
where a workstream deliberately did something other than what the finding
asked for.

### 1. "Where am I" has five representations — RESOLVED (S8, `cc44fd3..2a52dc0`)

`client/pane` owns ONE `Stack` of `Frame{grid, doorway tile, viewport}`.
Descending through any doorway pushes one, ascending pops one, and the
viewport you left a level at IS the frame you left. The URL (`url.go`) and
the pane-tile layout blob (`wire.go`) became ENCODINGS of that stack — one
codec each, both round-trip tested — and the bar's crumbs a projection, so
a crumb click is arithmetic (`AscentsTo`) rather than a bounded re-walk.
In the shim there is now one `descend(pane, tile, after)` — which kind of
frame it pushes is the DOORWAY TILE's own declaration, never the call
site's — and one `ascend(pane, n, animate)`. The eight ascents, the five
descents, `client/url`, `client/workspace`, `panestate.Saved` and the
`pane.Frame` push/pop family are deleted.

**Deliberate deviation: the pane-tile level is a SECOND axis and stays
one.** Entering a pane tile swaps the whole pane TREE, which is a
different gesture from walking deeper in one pane; it remains
`pane.Levels` with its own push and pop (`descendLevel` / `ascendLevels`),
session-ephemeral by #13. The finding's "a pane tile pushes a frame" was
not adopted. Everything else it named did fold.

Three bug classes went with the fold: the framing writeback now resolves
one way (`pane.FramingTarget`) for the settle persister and every ascent
alike; stacking an ephemeral visit over a live descent is a plain push,
so the stash/re-anchor dance that #208 rode in on is gone; and the two
ascent-history depths a spec had to assert separately are one number.

### 2. Framing is stored three ways — RESOLVED (S4, `27bb535..622f8e6`)

One shape (schema v11): a float CENTER in the grid's own coordinates plus
the pane-size-independent intrinsic zoom, on the row that OWNS the doorway
— `tiles.view_cx/cy/zoom` for a well (interior, exit or link: each doorway
keeps its own) or `grids.root_cx/cy/zoom` for a root that has none, home's
at the empty namespace. One store writer (`Store.SetFraming`), one wire
verb (`SetFraming`, whose target is also what routes it), one client
function (`persistFraming`). `system.root_view_*`, the integer window
origin, `zoomtrans.ViewOriginFromCenter` and its idempotence property test
are gone; the v11 rebuild converts every stored origin into the centre the
client already derived from it, so no grid moved.

**Deliberate deviation:** `SetTile`'s well arm was not deleted but turned
into a REFUSAL naming `SetFraming`, so the kind→operation mapping stays
total. The pane arm is a refusal for the same reason.

### 3. `version` is doing two jobs — RESOLVED (S5, `fe69fb4..b4bd5c0`, `00434b6`)

`version` means the user's content bytes and nothing else. `claimContentVersion`
+ `finishContentEdit` are reachable only from `WriteContent`'s text and url
arms and `RenameTile`; every other mutation loads through `loadForWrite`
and finishes at `emitTileChanged`, with no version to pass and no bump to
make. Captures, framing AND layout carry no claim — layout by evidence,
since the one thing a race could corrupt (two tiles in one cell) is
refused by the overlap check in the same transaction either way. The
request fields that used to carry those claims are `reserved`, so a stale
claim is unrepresentable rather than ignored, and `version_rule_test.go`
counts how many store writes can even be handed a version.

The dispatch layer collapsed as the finding predicted: `claimOnce`,
`doFreezeWrite`, `postVoidPersist`, `postCrossGridMutate`,
`postTwoGridMutate`, `doTileMutate` and `reactFraming` are gone;
`client/pending` and the cache's dirty-content ledger became ONE
`client/outbox` — one record, one reconcile rule, one retry kick, and the
unload flush drains the same list (which fixed a real hole: writes an
earlier outage had parked used to die at quit).

**Deliberate deviations:** `SetTileRequest.version` stays, documented down
to its one reader, the rename arm; `WriteContentRequest.version` stays and
its pane arm ignores it — the one place a stale claim is still
expressible and deliberately not read, pinned as such.

### 4. Three copies of Tile — RESOLVED (S6, `b1be3fe..71f6976`)

The proto is the owner. `rpc.Tile`, `rpc.Grid` and every request/response
whose Go shape is the message field-for-field are GENERATED into
`api/rpc/wire_gen.go` by an in-repo protoc plugin that `buf generate`
runs, with `make proto-check` catching staleness; `conv.go`'s mirror half
and the round-trip drift lint are deleted. The store's five column lists
are one descriptor (`internal/local/store/columns.go`) that renders the
DDL and drives the SELECT, the scan, the clone INSERT and every rebuild
copy list. The derived wire fields are inventoried with one owner each
(`ARCHITECTURE.md` §7.1), machine-checked by `TestDescriptorMatchesProto`.

**Deliberate deviation:** "use `pb` types everywhere" was rejected on
measurement, not taste — the wasm binary is 33.1 MB either way, and
`pb` messages embed a mutex, so `go vet` copylocks would have forced
`rpc.Tile`'s 197 value uses into shared mutable pointers inside the
client's caches. The Event oneof, the embedded Framing and the per-kind
create/set sugar stay hand-written, and `conv.go` now says why.

The class bit while the work was in flight: `insertTileCopy`'s hand-listed
INSERT had been silently dropping `content_zoom`, `url_history` and
`alt_user` from every clone. The copy is by name now and refuses an
incomplete value map.

### 5. Home crosses a gRPC hop to reach itself — RESOLVED (S2, `9be7b4d..29eeda1`)

Inside the node a namespace is a Go value: `internal/namespace` is the
gridwell.v1 method set with the four stream shapes decided once as
idiomatic Go, implemented by `local.Plugin`, `pluginhost.Adapter` and
`remote.Server`. `compose.ServeInProcess` — the bufconn loopback — is
deleted, and with it the SECOND ROUTER: the node export used to
re-implement every stream and hand-delegate every unary, and both doors
are now thin codecs over one `namespace.Namespace` (`server/router.go`),
so there is nothing to drift. gRPC survives at exactly the two hops that
cross a boundary and must serialize anyway — the plugin.v1 subprocess and
the federation socket — with one adapter each way. `Registry.SetTransit`,
`InfoResponse.transit` (reserved 15) and `internal/plugin/proxytest` went
with it: the transport is the only transit namespace, structurally.

The class the wire used to hide came with it: with no serializer between a
caller and a namespace they share protos by pointer, so qualification must
CLONE. That contract is written down in `internal/namespace` and pinned by
`TestTwoSubscribersEachSeeExactlyOnePrefix`.

**Deliberate non-change:** the subprocess door is untouched. It is the
part that admits strangers, and it is the only reason go-plugin was
chosen.

### 6. Two remembering systems — RESOLVED (S7, `d3e081d..8a39b01`)

`internal/plugin/mountcache` became `internal/sourcecache`: one engine,
one file (`Open(path)` → a `*Store` holding the single `cache.db` handle;
`Store.Front(ns, opts)` puts a `*Layer` in front of one namespace), with
whole-source PREFETCH demoted from part of the engine to a per-namespace
policy that only the transport opts into. `node.Start` is the one place
that decides who is cached. The adapter's own listing memory —
`Adapter.listing`, `cachedListing`, `unionEntries`, the kind memo and the
store's `CacheListing`/`CachedListing`/`RetiredKeys` — is deleted.

**Deliberate deviation, and the most important one in this list: the fold
is NOT "cache the adapter's answer".** That answer is a JOIN of two
owners' facts, and replaying it replays the OLD placement over a move the
user made while the source was dark. The split follows WHOSE fact is
missing instead:

- a dark SOURCE (the plugin answers; its directory, API or process table
  does not) costs only what the source says — the adapter merges an EMPTY,
  non-authoritative listing, so nothing is authoritatively absent, Merge
  retires nothing, and every row it minted reads back with the same ids
  and placement, INCLUDING a move made during the outage, stamped stale.
  The durable rows were always the memory;
- a dark PLUGIN (the subprocess is gone) or a dark node costs the
  node-side answer too, and `sourcecache` — now in front of every plugin
  as well as the transport — serves what that namespace last said,
  handshake included.

A rule fell out and is pinned: an answer already stamped stale is never
remembered, because storing it would overwrite the good answer it
degraded from with nothing left to correct it.

### 7. The forever file carries disposable and dead things — RESOLVED (S1 + S7)

| What | Where it went |
|---|---|
| the `session` table | dropped, schema v10 (`b95a6c1`) — both RPCs died 2026-07-26 |
| `tiles.configure_plugin_id` + the childless-well CHECK branch | dropped, schema v10; the verbs (`CreatePluginWell`, `AdoptChildGrid`, the SetTile adopt arm) went first in `0a42c34`. Stale unconfigured wells are ADOPTED, each given a fresh empty child grid, so the user's tile stays at its id in its cell and now opens |
| `object_id` on tiles and grids | dropped, schema v10, end to end — both proto fields reserved, the five Create\*Request provenance fields, the server's cross-plugin copy stamps, the client's link-drop requests and the Electron bridge's `placeWebview` objectId |
| `create_schemas` (`Grid` and `InfoResponse`) | reserved, `6471041` — no producer, no reader |
| menu-entry CREATION (`kind` + `param_schema`, `client/schemaform`, `entry_params.go`, `modal_form.go`, `CreateEntryTile`) | deleted, `63d47bf`. The ROOT entry shape (`grid_id` set — local's trashcan, #262) is live and untouched |
| the `listings` table | dropped, schema v12 (`2af8e19`), after S7 removed its last reader |

The drops are the first migrations in this store's history that remove
anything, so they carry an owner decision (`docs/simplify-plan.md`) and a
rule rather than a precedent: EVIDENCE that no released binary reads the
storage for a user-visible meaning; a REBUILD migration preserving every
surviving row and the `sqlite_sequence` seeds; a row the new shape cannot
hold CONVERTED, never deleted; the decision recorded in the migration's
chain-entry comment; the wire field `reserved`, never renumbered.
`internal/local/store/CLAUDE.md` carries it, and each drop is pinned
against a GENUINE old file (a chain-built one already has the new shape).

Two latent bugs surfaced under the first drop and were fixed with it:
`Open` ran `bootstrapRoot` BEFORE the migration chain (a write through the
current column set onto a file that did not have it yet — harmless only
while every change was additive), and `rebuildTiles` recreated only
`tilesIndexDDL`'s indexes while `DROP TABLE` also takes v9's
`idx_tiles_live_key`.

### 8. Shells break "the same everywhere" — RESOLVED (S3, `e1a4a01..802c09f`)

PTY bytes ride a WebSocket on the WEB door: `GET <origin>/shell?tile_id=…`
on the same cookie-gated mux as every other page request, same-origin
checked, with the bind on the handshake so the attach is atomic with the
upgrade. The grammar has one owner both ends read (`client/shellwire`),
the lifecycle is a js-free package with the Electron rules ported into it
and tested (`client/shellstream`), and both doors — this one and the node
export's bidi gRPC — enter through ONE shell route, so `disable_shells`,
id resolution and federation cannot hold on one and not the other. The
Electron shell stack (`shellgrpc.ts`, `shellstreams.ts`, four IPC
channels, the gRPC dependencies) is deleted, and with it the desktop's
only reason to know the federation socket exists. `caps.LiveShell` is now
simply `!shellsDisabled`: a browser and a phone attach a real terminal.

**Deliberate non-change:** `mobile.Start` still sets `disable_shells`.
What a phone cannot HOST (there is no tmux on iOS) was never the same
question as what its client can REACH, and reaching a shell on another
node is unaffected.

Three real bugs the transport change exposed, each fixed with a test:
routing before the upgrade could acquire a PTY for a connection the origin
check then refused (found by the new seam test); a takeover reused the
same pty so tmux never repainted for the new viewer, which the old
single-IPC-queue ordering had been hiding; and the door cancelled the
attachment before writing the exit frame, so "this session is gone"
sometimes arrived as a bare EOF.

### The shape to aim for — where it stands

All six are in force: one place model (S8), one framing representation
(S4), versions on content bytes with one outbox (S5), namespaces as Go
interfaces (S2), a pruned durable schema with `listings` out of the
forever file (S1 + S7), one transport with shells included (S3). The two
places the tree deliberately differs from the shape as written are the
pane-tile second axis (finding 1) and the dark-source split (finding 6),
both argued above.
