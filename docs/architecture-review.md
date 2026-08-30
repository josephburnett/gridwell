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

`pb.Tile` ↔ `rpc.Tile` (hand-mirrored in `api/rpc/conv.go`, pinned by a
drift lint) ↔ the store's scan/insert column lists. Ten of the wire
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
