# Gridwell Architecture

This is the map of how Gridwell works: the layers, the contract between
them, and the invariants each one holds. `README.md` says what Gridwell is
and how it feels to use. `CLAUDE.md` says how to change the code without
breaking it. This document says how the machine runs.

One diagnosis to carry with you: the store and server are sound by
construction. Every fragile invariant lives in the client/native split, and
they all share one root cause — **a single fact stored in several parallel
copies, written from many code paths.** The cure is one discipline: derive a
fact in exactly one place and read it everywhere (§7). The seam catalog (§8)
lists where that still needs doing.

---

## 1. The system in one breath

```
┌──────────────────────────────────────────────────────────────────────┐
│ Electron main process        apps/desktop/src/main/                  │
│   native WebContentsViews positioned over a wasm canvas              │
│   (live URL pages) + the shell PTY relay. Invisible to `make check`. │
└───────────────┬──────────────────────────────────────────────────────┘
                │  IPC  (window.gridwell bridge, CSS-px bounds both ways)
┌───────────────▼──────────────────────────────────────────────────────┐
│ Go→wasm client               client/wasm/  +  pure client/* packages │
│   canvas, panes, gestures, framing, previews, menu                   │
│   ~13.6k LOC of orchestration with no unit tests — see §5            │
└───────────────┬──────────────────────────────────────────────────────┘
                │  Connect-RPC  (the Gridwell service)
┌───────────────▼──────────────────────────────────────────────────────┐
│ Local server                 internal/server, api/rpc               │
│   STATELESS router: splits <uuid>/<id>, forwards, re-qualifies       │
└───────────────┬──────────────────────────────────────────────────────┘
                │  go-plugin gRPC  (the SAME Gridwell service)
┌───────────────▼──────────────────────────────────────────────────────┐
│ Namespaces     home (internal/local) · connections (internal/remote)  │
│   · plugins/{fs,proc,gitlab} as CONTENT PLUGINS (plugin.v1)          │
│   a plugin is stateless (keys + content); the node mints ids and      │
│   keeps every namespace's arrangement in the ONE store (gridwell.db) │
└───────────────┬──────────────────────────────────────────────────────┘
                │
┌───────────────▼──────────────────────────────────────────────────────┐
│ Store                        internal/local/store/  (node code, v2)  │
│   ids, version = the user's content bytes only, clone, blobs         │
│   SOLID BY CONSTRUCTION — the model to emulate                       │
└──────────────────────────────────────────────────────────────────────┘
```

**One contract, every hop.** `api/gridwell/v1/data.proto` defines a single
17-RPC service implemented identically by the local server, every
namespace, and a remote node reached through a connection. "Remote" adds
no vocabulary — the transport forwards to a remote node's **export**
(`nodeexport.go`: the same service over raw gRPC, routed by the qualified
ids each request carries). Ids chain one segment per hop
(`<id>/<conn>/<remote-id>/<tile>`), so any depth of mounting routes
generically. Every byte — content streams and the live PTY
included — crosses this one interface.

**A node has no grid of its own** (2026-08-29, `docs/one-node.md`; the
node grid — `<node_id>/0`, one link tile per plugin — was deleted). The
client boots into **home**, the first configured entry's root grid
(`rpc.HomeGrid`); a mount lands there too (the export's `Info.RootGridId`
is the same derivation). Plugins are reached from the + menu's top row
(click = portal descent, drag = drop an exit-well link).

---

## 2. The contract (`api/gridwell/v1/data.proto`)

The proto is the single source of truth for both the wire types and the
persisted record shapes. Every field on `Grid`/`Tile` maps 1:1 to a column
in `internal/local/store/schema.go`; a drift-lint test fails the build if they
diverge.

The surface, one method per concept:

| Group | Methods |
|---|---|
| Lifecycle | `Info`, `Probe`, `Handshake` |
| Framing | `SetFraming` — the ONE framing write: a float center plus a pane-size-independent zoom, onto the DOORWAY tile a grid was entered through or onto the grid row itself for a root that has none |
| Reads | `GetGrid`, `GetTile`, `GetTilePreview` |
| Content bytes | `ReadContent` / `WriteContent` — the ONE way content moves. Versioned; a write commits at close (a broken stream leaves the old value intact); a read on a leaf link resolves to the target at the serving node |
| Web content | `ServeContent` — the RPC carrier behind the HTTP `/content/<token>/<tile-id>/<subpath>` door: a plugin serves ANY content as web content (an image, a whole HTML page with relative subresources). GET-only; routes/link-resolves/federates exactly like `ReadContent`. The door stamps `CSP: sandbox allow-scripts` (opaque origin — no cookies, no RPC reach) and gates by the content token (its own password derivation, handed out on the authenticated `Handshake`). `Tile.serves_page` (wire-only, plugin-derived) tells the client to present the descent with url-tile semantics at the derived address |
| Mutations | `CreateTile` (metadata only — a body follows as a WriteContent), `SetTile` (framing/preview + rename + content_zoom, one op per call), `PlaceTile` (the one placement writeback), `CloneTile`, `DeleteTile` |
| Live bytes | `OpenShell` (a PTY both ways — deliberately the one live wire), `ShellSessionAlive` |
| Events | `Subscribe` |

No request carries a descent path — the server derives location facts from
rows it owns. Sessions and networks never cross the wire: the Chromium
session is host-local and live tiles browse from the host's own network.

Three fields encode the product rules directly:

- `Tile.view_cx / view_cy / view_zoom` — at once the preview frame, the
  descent target, and the ascent return value. One value, three readings.
  ONE shape everywhere (schema v11): a float CENTER in the child grid's
  coordinates plus a pane-size-independent zoom (the intrinsic ratio
  live/overtake). A grid with no doorway keeps the same three numbers on
  its own row (`grids.root_cx / root_cy / root_zoom`, home's at `ns = ''`),
  and one store writer (`Store.SetFraming`) writes both. Zoom 0 is the one
  "never visited" convention.
- `Tile.reference` (bool, server-derived, never stored) — the one
  authoritative "this tile is a link" signal. Render draws the dashed
  border from it, delete and clone key on it, descent portals through it.
- `Grid.writable` (bool, wire-only) — the owning plugin's per-grid mutation
  capability, stamped by the serving node. The client's "+ palette shows
  here" gate reads the grid, never a local plugin-list lookup, because one
  ssh mount fronts many remote plugins with differing capabilities.

---

## 3. The server: a stateless router (`internal/server`)

The server holds no Gridwell state. It routes every call to the owning
plugin and translates ids at the boundary:

1. `Server.resolve(id)` peels the FIRST segment: the node's own id
   followed by a numeric segment is HOME; the node's id followed by a
   letter-leading segment is a CONNECTION (the transport peels that
   segment next); any other first segment is a plugin. The remainder
   passes through verbatim, so `<id>/<conn>/<remote-id>/<tile>` chains
   resolve one hop at a time.
2. The namespace answers in its own id space — bare ints for home and
   plugins, chains for the transport (whose ids arrive already qualified
   from the remote's perspective).
3. `qualifyTilesFor` re-qualifies ids going out: the leaf rule (prefix ids,
   derive `reference`) or the transit rule (prepend one segment everywhere,
   trust the wire bits — `rpc.TransitQualifyTiles`, one shared
   implementation). Transit is the namespace's TYPE (the transport), not
   a declaration, so it holds while the remote is down.

**Two wire surfaces, one implementation, two doors.** `WebHandler` is the
Connect handler behind the password gate — the `web.bind` listener,
bindable to a network; `FederationHandler` is the raw-gRPC node export,
whose unary methods delegate straight into the Connect handler and whose
streams (`OpenShell`, `WriteContent`) route by the id in their first
message — served on its own listener, the 0600 unix socket at
`federation.socket`, never TCP (the kernel gates it to the owning uid;
ssh's direct-streamlocal forwarding is the authenticated transport
between nodes). The web door always has a password — the 0600
`web-password` file serve mints and prints; delete it to rotate. A remote mounter and a browser exercise the same routing
code through different doors.

`Info` handshakes are timeout-bounded and cached per uuid after first
success (invalidated on a ROOT `SetFraming`, since root framing rides
the handshake). Capabilities (`watch`, `writable`, `has_session`) are facts a
plugin declares once in `Info`, never re-derived from its kind string.

---

## 4. The namespaces and the store

**The node is its home** (docs/one-node.md). `server.yaml` names the
node's `id`, its `connections:` and its `plugins:`; a missing file is a
fresh home (serve mints the id and writes the file). The node constructs
its own home (`internal/local`) and transport (`internal/remote`) from
that config — they are not plugins and never appear in `plugins:`. Every
`plugins:` entry is a CONTENT PLUGIN (`plugin.v1`): spawned as a
`gridwell-plugin-<kind>` subprocess by the stock host, or composed
in-process by a leaf binary that bundles it (the mobile bind; the compose
door hides which). A plugin is stateless — it answers in its own stable
keys — and the node keeps its memory as ONE namespace of the ONE store
(`internal/local/store`, `<home>/gridwell.db`; docs/one-node.md §2.6)
that mints the ids and holds the arrangement. The store is opened
identity-verified (`local.OpenVerified` against the node's id), so the
id every stored reference carries is the id the node answers with; a
plugin's memory needs no verification of its own — it is a namespace of
that same verified store. A pre-one-node home converts itself at first
serve (`node.Convert`).

- **home** (`internal/local`; né localdb) owns all user content (text,
  urls, wells, pane tiles) plus shells and the event stream — the one
  writable namespace. Shell tiles are tmux sessions on a private per-node
  socket, so they survive restarts.
- **fs / proc** project the filesystem and process table as read-only
  grids, mapping paths/PIDs to stable integers. Both enforce the sweep
  rule: a failed read never deletes a tile row — only a definite GONE does.
  An unreadable source serves its stored rows verbatim until it's readable
  again.
- **the transport** (`internal/remote`; né the ssh plugin, folded into
  the node 2026-08-16, finished 2026-08-29 — docs/one-node.md): the
  node's connections to other nodes. A connection is server.yaml CONFIG
  (`connections:` — an immutable name, a label, how to dial); the
  transport dials each at boot (bounded — the boot doesn't serve
  mysteries), learns where it lands (the remote's HOME) and remembers only
  that plus the graveyard of retired names (`retired_names`: a name never
  returns). It owns no tiles and no grid: a connection is a row in the +
  menu (`HandshakeResponse.connections`, uuid `<id>/<conn>`) and, when
  dragged, an ordinary link tile in the user's grid. Every reference
  through it is `<id>/<conn>/<remote-id…>`: `Server.resolve` peels the
  node's own id and hands the rest to the transport, which peels the
  connection name and prepends it on the way back — the same transit
  rule at both hops. The mount cache (`internal/plugin/mountcache`) fronts
  the transport so a dark remote degrades to stale-but-readable.

### 4.1 `version` means the user's content bytes — the best-enforced invariant

`version` says ONE thing (owner decision 2026-08-29,
`docs/simplify-plan.md` S5): **the user's content bytes changed.** It is the
optimistic-concurrency claim for exactly those edits and nothing else.
Three named helpers in `internal/local/store` make that structural rather
than advisory:

| Helper | Used by | Claims? | Bumps? |
|---|---|---|---|
| `claimContentVersion` + `finishContentEdit` | content writes: `WriteContent`'s text and url arms, `RenameTile` | Yes | Yes |
| `loadForWrite` + `emitTileChanged` | everything else | No | No |

"Everything else" is three families, and each was a real bug source when it
carried a version:

- **Captures** — a page title, a preview jpeg, a url trail, a shell's
  foreground command. Facts the server OBSERVED. They ride the tile event
  to every client as last-writer-wins. A capture that bumped could cost a
  concurrent editor their claim mid-paragraph.
- **Framing** — `view_*`, a text tile's window and mode, content zoom, a
  standing url freeze, a pane tile's layout. Last-writer-wins by design.
- **Layout** — place / move / resize / clone / delete. An explicit act on a
  tile the user can SEE, so a race resolves as "whoever moved it last moved
  it"; the one thing a race could corrupt — two tiles in one cell — is
  refused by the overlap check inside the same transaction, claim or no
  claim.

A new mutation physically has to choose a side (there is no version to pass
to `loadForWrite`), and the wire agrees: the request fields that used to
carry a claim for framing and layout are `reserved`, so a stale claim is
not ignored — it is unrepresentable. `version_rule_test.go` is the whole
table, and it counts how many store writes can even be handed a version, so
a claim cannot reappear silently. This is what enforced-by-construction
looks like; emulate it.

### 4.2 Identity and clone

- Grid/tile/blob ids are SQLite AUTOINCREMENT, never reused. Stored
  references and client caches key on them, so reuse would be catastrophic.
- Clone is an eager deep copy: new ids for the copy, blobs shared by
  content address + refcount, no structural sharing. An edit to one copy
  can never touch another, and no id is ever reassigned. (COW was tried and
  torn out — a fork re-rows tiles, and no patch makes that safe.)
- The storage format is frozen and additive-only; the contract lives in
  `internal/local/store/CLAUDE.md`. Never delete a DB to absorb a change.
- One layering wrinkle: `internal/local/store` imports `client/markdown` for
  `AltFromSource` (deriving a text tile's label from its first line). Pure
  and shared, but the arrow points from persistence into the client tree —
  if it grows, move the derivation to a neutral package.

---

## 5. The Go→wasm client (`client/wasm` + pure `client/*`)

The intended shape: a thin wasm shim over pure, headlessly testable
`client/*` packages (`pane`, `cache`, `zoomtrans`, `gesture`, `wsbar`,
`markdown`, `menu`, …). The pure packages are clean and well-tested.

The shim never stayed thin. `client/wasm` is 29 files, ~13.6k LOC, zero
test files. `make check` compiles it (`GOOS=js`) but executes none of it;
only the e2e gates touch it, as a black box. The hottest files in the repo
live here (`input.go` ~2,400 LOC, `render.go` ~1,300, `main.go` ~1,200).
When you change behavior here, extract the decision into a js-free
`client/*` package and unit-test it — that rule is the charter, not a
suggestion.

**The `App` object** carries the session: the pane tree, per-pane local
state (`App.locals`, one entry per live pane, dropped atomically with the
pane), the grid/tile cache, and the native handles. Gesture handlers and
stream callbacks both mutate it with no serialization point — which is why
every fact here must have exactly one writer.

**Framing lives in five roles** that must agree for the round trip to be
idempotent: the live pane (`Cx/Cy/Zoom/Anchor/Path`), the ascent stack, the
server tile's `view_*`, the portal `Up` frames, and the URL bar. They are
kept consistent by convention; the round trip is locked by
`framing-roundtrip.spec.ts`. A debounced settle persister (armed from
`draw()` — every state change redraws, so there is no per-gesture hook to
forget) writes settled framing back without waiting for an ascent.

**Text content has one door.** A content-store entry ({bytes, base version,
dirty}, keyed by tile id) owns a text tile's current body. Every keystroke
mirrors into it; every flush goes through `text_flush.go`, which posts by
tile id and never reads the DOM — so bytes can never be saved under another
tile's id. Saves claim `SaveBasis` (advanced only by fetches and save
responses), so a stale save 409s and reconciles visibly — and since a
capture no longer bumps the row, a 409 there can only mean a real
concurrent edit, which is why `clientsync.ReactSave` surfaces it.
`cache.Apply` drops events strictly older than the cached row, and a newer
row drops a clean body while sparing a dirty one — a foreign writer's edit
appears, and unsaved typing survives.

**Every unacknowledged write has one home.** `client/outbox` is the ordered
record of what the server has not answered — framing, captures, layout, and
the user's unsaved bytes alike — with ONE reconcile rule (`Record`: a
transport failure parks a retry, any verdict acks) and two drains: the
retry kick on reconnect, and the unload flush, where each entry leaves
through its `navigator.sendBeacon` form. It holds order and retry, never a
copy of a value: a content entry's thunk re-reads the bytes from the cache,
their one owner. On the client side of a mutation there are exactly two
paths — `postWriteContent` (the one write that claims a version) and
`write`/`do` (everything else) in `client/wasm/mutate.go`.

**Events never touch framing.** The SSE path flows only into the cache;
framing writes live only in the gesture/transition code. An event landing
mid-animation updates data and redraws, but cannot move the viewport a
transition is animating. (This separation is verified by inspection only —
see I11.)

**The rendered view is a DOM overlay.** A focused text pane in rendered
mode shows sanitized HTML in a div (`markdown.RenderHTML`: goldmark, go-org
for `.org` names, bluemonday); the editing textarea is another overlay.
Task-list checkboxes are the overlay's one interactive control (2026-08-09):
a click maps DOM index → source marker (`markdown.ToggleTask`, one shared
parser, parity-pinned by unit test) and the edit rides the same content
entry + flush as a keystroke.
Every other view paints raw soft-wrapped source on canvas, wrapped to the
same columns the textarea shows (`markdown.WrapRawText`), so nothing
reflows when focus moves. Grid previews render at constant scale
(`markdown.PreviewWindowFrame` takes only the tile's own facts, so a
sibling pane's width cannot re-wrap a preview — unrepresentable by
signature).

**Every pane wears the bottom bar** (`client/wsbar` geometry,
`bottombar.go` glue; #267, 2026-08-21 — the band was focused-pane-only
under #220, but content resizing on every focus change was distracting;
focus shows only in the border color): workspace crumbs, the anchor
block, the descent chain as clickable square previews (derived per frame
from `pane.DescentChain` — never stored), the centered title, and the
circle slot (the + menu / back / refresh button). Native surfaces carve
the band out of their rects unconditionally (`panebox.BarInset`), so
nothing can occlude it and nothing reflows on focus moves. Clicks act in
the focused pane; a band click in an unfocused pane moves focus, nothing
else. Clicking a chain crumb is THE bar ascent gesture; middle-click on
a pane is the in-pane shortcut.

---

## 6. The Electron native shell (`apps/desktop/src/main`)

Live URL pages are native `WebContentsView`s positioned over the wasm
canvas. They are separate webContents off the main page, so nothing here is
reachable by `make check` — only `make check-electron` (harnesses under
xvfb) and `make check-e2e` (the full app). This is why the worst bugs (live
tiles, menus over live tiles) escape the fast gate.

**`WebviewRegistry`** (`webviews.ts`, ~600 LOC, the documented bug source,
still untested directly) owns a `Map<paneId, {view, bounds, hidden,
focused}>`. The tightest timing seam in the system is `syncURLViews`, run
every frame: the renderer sends CSS-px content-box bounds over IPC;
`roundBounds` snaps to integer DIP; `boundsEqual` skips no-op churn; and
`liveOverlaysHidden` (dragging ∨ rightDrag ∨ leftResize ∨ menuOpen) parks
native views off-screen so canvas overlays can paint where they sit. Get
that predicate wrong and a live view either eats input or vanishes.

A view's `focused` flag feeds one thing: the focus-steal guard — only the
focused pane's view may hold OS keyboard focus, and a page that grabs it
via scripted navigation gets it taken back.

**Sessions.** One host-local Chromium partition, `persist:gridwell`, for
every live url tile — local or through a mount. Chromium's own disk
persistence is the session's system of record (a documented charter-§7
exception, like processes and files). Teardown captures a final frame for
the freeze but never depends on the capture succeeding.

**Shell transport.** `shellstreams.ts` dials the sidecar's federation
socket (the banner's `federation=` path, `unix:`) and
relays gRPC `OpenShell` per pane over IPC (replace-on-open, at-most-once
exit, no-op after close — unit-tested against a fake dialer). Browsers get
frozen shell previews, caps-gated like live url tiles.

---

## 7. The cure pattern (already in the codebase — copy it)

Derive a fact in exactly one place and read it everywhere; never store the
same truth twice. The templates:

| Exemplar | The one fact | Where derived | Read by |
|---|---|---|---|
| `Tile.reference` | "this tile is a link" | `server.qualifyTiles` | render, delete, clone, descent |
| `loadForWrite` / `claimContentVersion` | "may this mutation claim a version" | `store/tiles.go` | every store mutation |
| `emitTileChanged` / `finishContentEdit` | "does this mutation bump version" | `store/tiles.go` | every store mutation |
| `outbox.Record` | "is this write still owed, or did the server answer" | `client/outbox` | every client mutation |
| `classifyStoreError` | "what status is this error" | one function | every transport |
| `zoomtrans.LiveFromIntrinsic` / `IntrinsicFromLive` | the viewport transform | one pure pair | preview + descent |
| `client/menu` | "is the menu open, on which pane" | one state machine | every gesture path (was 14 scattered writes) |
| `cache` content entries + `text_flush.go` | "the bytes, their version, and whether they're edited" | one entry per tile id | every save path |
| `shellconn.DecideAutoLive` | "does this descent go live" | one decision | every descent/restore path |
| `local.OpenVerified` | the plugin's identity | verify+open+inject fused | every identity read |

Each makes a bug class unrepresentable. That is the goal of every change:
prefer the design where the bug cannot be written over the design where it
is merely fixed this once.

---

## 8. The seam catalog: one fact, many copies

Each entry is the same disease — one truth duplicated — and a ranked target
for the §7 cure.

1. **Viewport / framing** — five roles kept consistent by convention (§5);
   the round trip is locked by `framing-roundtrip.spec.ts`. Highest impact:
   this is the descend/ascend round trip.
2. **Native view bounds vs. canvas pane rect** — the per-frame
   reconciliation in `syncURLViews`; coordinate math in two languages,
   timing-sensitive. The pure math is extracted and tested (`viewutil.ts`).
3. **The drag threshold** — `dragThreshold` (Go, the declared owner) plus
   two forced copies (`viewutil.ts`, and inlined in the sandboxed
   `urlview-preload.ts`, which cannot import). Drift-linted by
   `gesture-threshold.test.ts`.
4. **The `SetTile` kind→operation mapping** — described in the proto,
   implemented in the local plugin switch, and again in `conv.go`.
5. **Text scroll of a rendered descent** — the canvas wheel handler writes
   `p.TextScrollY` and the rendered overlay's own scroll listener writes it
   too; the canvas path never syncs the div's scrollTop. Two writers, found
   2026-07-31, unfixed.

Cured and closed: the menu (single owner `client/menu`); the corner-control
visibility predicate (the control views were deleted outright, #214); the
source-sweep policy (fs/proc, both tested); plugin capabilities (declared
once in `Info`).

---

## 9. Known drift (do not trust these names)

The rule: fix a stale comment or name in the same commit that touches the
file. What remains:

- **Three legacy JSON keys on `pane.Pane`.** `TextScrollX/Y` and `TextZoom`
  tag as `file_scroll_x`/`file_scroll_y`/`file_zoom` while every neighbor
  (and `pane.Frame`, `panestate.Saved`, the persisted `LayoutV1` codec)
  says `text_*`. Inert today — nothing marshals `pane.Pane` itself — but a
  hazard the moment something does. Finish the rename when touching the
  struct.

---

## 10. Map of the key journeys

- **Boot.** `Handshake` returns home (`home_grid_id`, a field), the
  plugins and the connections; panes anchor at home and "/" is its URL. A
  pane's URL is its anchor as leading path segments, then tile ids:
  `/<plugin>/<grid>/3/4`. Leading non-numeric segments are the namespace
  chain — sound because a plugin id can never be purely numeric.
- **Descend into a well.** The pane reads the tile's `view_*`, pushes its
  current state onto the ascent stack, appends to `Path`, restores the
  stored viewport. A link tile descends as a PORTAL instead: push an `Up`
  frame, swap the anchor to the link's target — so path ids never mix
  namespaces.
- **Descend into content.** Entering a url tile reopens the page; a shell
  reconnects its still-running tmux session (a fresh tile creates one; a
  dead session stays frozen). One owner decides (`shellconn.DecideAutoLive`)
  and every re-entry path — reload restore, workspace swap, a stacked
  ascent — applies the same rule. The frozen preview is what a tile looks
  like from outside; going live never mutates the row.
- **Ascend.** The intrinsic viewport writes back via `SetFraming` (framing —
  no claim, no version bump); a url/shell descent freezes its preview (an
  automatic capture — also no claim and no bump); the parent frame pops. A portal ascent writes through the
  containing link tile, or the root grid row when there is none (the same
  verb, the other target). Clicking a bar
  crumb ascends all the way to that level.
- **Drop a tile.** Gesture → `CreateTile`/`PlaceTile`/`CloneTile`,
  id-addressed (layout carries no version claim) → server routes → store mutates →
  `Subscribe` event → `cache.Apply` → redraw. Across a plugin boundary
  there is no move: a left-drag creates a LINK in the destination (exit
  well or `link_target_id`), a right-drag CLONES (leaves copy bytes; a
  solid well deep-copies its subtree — `internal/server/deepcopy.go`,
  issue #200 — degrading any unreachable piece to a LINK to the original,
  never a silent hole; a source that answers "gone" still aborts).
  Relocation is the explicit two-step: clone, then delete.
- **Open a live URL tile.** The canvas places a rect; IPC asks the native
  layer for a `WebContentsView` on the shared partition; `syncURLViews`
  tracks its bounds every frame and parks it during overlays.
- **Enter a workspace (pane tile).** The third descent verb: flush every
  outer leaf, push a `client/workspace` frame (outer tree + origin), decode
  the layout blob, swap `App.tree`. While inside, a debounced persister
  encodes the live tree, hash-diffs, and posts the layout as a
  `WriteContent` (framing-class — no claim, never bumps version) only on
  change. The
  URL is `?w=<tile id>`. The workspace stack itself is session-only, like
  portal frames.
- **Show the menu.** `menu.Open(paneID)` on the focused pane, toggled from
  the bar slot; native views park; the popover paints above every pane. A
  click inside the open popover routes BEFORE pane resolution — resolving
  the pane under it first would transfer focus and close the very menu
  being used.

---

## 11. The invariant inventory

Construction-enforced invariants are safe to build on; convention-only
invariants are where bugs are born.

| # | Invariant | Enforced where | Status |
|---|---|---|---|
| I1 | Ids never reused | SQLite AUTOINCREMENT | ✅ construction |
| I2 | Framing write ≠ content edit | `emitTileChanged`/`finishContentEdit` | ✅ construction |
| I3 | Clone is an eager deep copy; no id reassigned | `CloneTile` | ✅ construction |
| I4 | Blobs immutable, content-addressed, refcounted | store blob layer | ✅ construction |
| I5 | "Is a link" is one derived fact | `qualifyTiles` → `Tile.reference` | ✅ construction |
| I6 | Qualified-id routing | server `route` + transit rules | ✅ construction |
| I7 | preview = descent target = ascent return | five client roles synced by convention | ⚠️ convention, round trip tested (`framing-roundtrip.spec.ts`); the preview-bytes half still has no oracle (issue #19) |
| I8 | Text preview == what you left (no re-wrap) | `PreviewWindowFrame` takes only the tile's own facts | ✅ construction + tested |
| I9 | Focus steal is impossible | the registry's focus guard; wasm owns focus | ✅ tested (`control-focus.spec.ts`) |
| I10 | Menu changes only by user action | one owner `client/menu` | ✅ construction + tested |
| I11 | Reading never mutates (SSE during animation) | events flow only into `cache`; framing writes only in gesture code | ⚠️ separation verified by inspection only — no mid-transition injection test; a new framing write into the SSE path would regress silently. The echo/foreign-writer reconcile half is ✅ (`client/cache` units + `foreign-writer.spec.ts`) |
| I12 | User state survives an unreachable source | fs/proc sweep rules | ✅ construction + tested |
| I13 | A workspace restores exactly as left; a pure visit never writes | live tree is the one owner; the blob is derived (encode + hash-diff) | ✅ construction + tested |
