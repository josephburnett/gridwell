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
│ Local server                 internal/server, internal/rpc           │
│   STATELESS router: splits <uuid>/<id>, forwards, re-qualifies       │
└───────────────┬──────────────────────────────────────────────────────┘
                │  go-plugin gRPC  (the SAME Gridwell service)
┌───────────────▼──────────────────────────────────────────────────────┐
│ Plugins        internal/plugin/{localdb,fs,proc,sshhost,proxy}       │
│   each a separate binary owning one SQLite DB + one id space         │
└───────────────┬──────────────────────────────────────────────────────┘
                │
┌───────────────▼──────────────────────────────────────────────────────┐
│ Store                        internal/store/                         │
│   ids, the framing-vs-content version split, clone, blobs            │
│   SOLID BY CONSTRUCTION — the model to emulate                       │
└──────────────────────────────────────────────────────────────────────┘
```

**One contract, every hop.** `api/gridwell/v1/data.proto` defines a single
17-RPC service implemented identically by the local server, every plugin,
and a remote node reached over SSH. "Remote" adds no vocabulary — the ssh
plugin forwards to a remote node's **export** (`nodeexport.go`: the same
service over raw gRPC, routed by the qualified ids each request carries).
Ids chain one segment per hop (`<ssh>/<plugin>/<id>`), so any depth of
mounting routes generically. Every byte — content streams and the live PTY
included — crosses this one interface.

**Every node exposes a node grid.** A node's plugin list is a real,
read-only grid — `<node_id>/0`, served by `internal/server/nodegrid.go` —
one dashed link tile per plugin. It is the federation surface: an ssh mount
lands on the remote's node grid. It is NOT the local landing page: the
client boots into **home**, the first configured plugin's root grid
(`rpc.HomeGrid`), and plugins are reached from the + menu's top row (click
= portal descent, drag = drop an exit-well link).

---

## 2. The contract (`api/gridwell/v1/data.proto`)

The proto is the single source of truth for both the wire types and the
persisted record shapes. Every field on `Grid`/`Tile` maps 1:1 to a column
in `internal/store/schema.go`; a drift-lint test fails the build if they
diverge.

The surface, one method per concept:

| Group | Methods |
|---|---|
| Lifecycle | `Info`, `Probe`, `ListPlugins`, `SetRootView` |
| Reads | `GetGrid`, `GetTile`, `GetTilePreview` |
| Content bytes | `ReadContent` / `WriteContent` — the ONE way content moves. Versioned; a write commits at close (a broken stream leaves the old value intact); a read on a leaf link resolves to the target at the serving node |
| Mutations | `CreateTile` (metadata only — a body follows as a WriteContent), `SetTile` (framing/preview + rename + content_zoom, one op per call), `PlaceTile` (the one placement writeback), `CloneTile`, `DeleteTile` |
| Live bytes | `OpenShell` (a PTY both ways — deliberately the one live wire), `ShellSessionAlive` |
| Events | `Subscribe` |

No request carries a descent path — the server derives location facts from
rows it owns. Sessions and networks never cross the wire: the Chromium
session is host-local and live tiles browse from the host's own network.

Three fields encode the product rules directly:

- `Tile.view_x / view_y / view_zoom` — at once the preview frame, the
  descent target, and the ascent return value. One value, three readings.
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

1. `route(id)` peels the FIRST segment of a qualified id; the remainder
   passes through verbatim, so `<ssh>/<plugin>/<id>` chains resolve one hop
   at a time. The node's own uuid routes to the in-process node-grid
   provider.
2. The plugin answers in its own id space — bare ints for a leaf, chains
   for a transit plugin (a node mount, whose ids arrive already qualified
   from the remote's perspective).
3. `qualifyTilesFor` re-qualifies ids going out: the leaf rule (prefix ids,
   derive `reference`) or the transit rule (prepend one segment everywhere,
   trust the wire bits — `rpc.TransitQualifyTiles`, one shared
   implementation). Transit-ness comes from `Registry.Transit`, a
   config-time fact, so it holds while the remote is down.

**Two wire surfaces, one implementation.** The Connect handler serves
browsers; `NodeHandler` wraps the same mux in h2c and routes raw gRPC to
the node export, whose unary methods delegate straight into the Connect
handler and whose streams (`OpenShell`, `WriteContent`) route by the id in
their first message. A remote mounter and a browser exercise the same
routing code.

`Info` handshakes are timeout-bounded and cached per uuid after first
success (invalidated on `SetRootView`, since root framing rides the
handshake). Capabilities (`watch`, `writable`, `has_session`) are facts a
plugin declares once in `Info`, never re-derived from its kind string.

---

## 4. Plugins and the store

**Spawn model.** `server.yaml` is mandatory. Every configured plugin is
spawned as a go-plugin subprocess — the only production path; the
in-process loader survives solely as a test harness. Each plugin owns one
SQLite DB and one id space; identity is verified at spawn and injected into
the store in one fused step (`localdb.OpenVerified`), so the id every
stored reference carries is the id the store answers with.

- **localdb** owns all user content (text, urls, wells, pane tiles) plus
  shells and the event stream. The only writable plugin. Shell tiles are
  tmux sessions on a private per-DB socket, so they survive plugin
  restarts.
- **fs / proc** project the filesystem and process table as read-only
  grids, mapping paths/PIDs to stable integers. Both enforce the sweep
  rule: a failed read never deletes a tile row — only a definite GONE does.
  An unreadable source serves its stored rows verbatim until it's readable
  again.
- **ssh** (#199, #251): the multi-connection plugin
  (`internal/plugin/sshhost`) — PARAMETERIZED: no root grid; its connection
  list is declared as its INSTANCE grid (`PluginInfo.instance_grid_id`),
  the storage address the client's instance picker reads and writes, never
  a landing page. Each connection is a well whose params are its CONTENT
  and whose minted short id is a sub-namespace segment
  (`<ssh>/<conn>/<remote-plugin>/<id>`), peeled and prepended with the same
  transit rule the server applies one level up. A params document that
  canonicalizes equal to a live connection's is refused at commit — one
  param-set, one connection. Connections dial lazily and self-heal;
  deleting one tombstones its segment forever. Connections are NEVER
  config: `init` refuses the old host: keys, and `gridwell serve` migrates
  a pre-#251 config entry into a connection row at boot (the config→data
  twin of a schema migration).

### 4.1 Framing ≠ content — the best-enforced invariant

In `internal/store/tiles.go`, two named helpers split every mutation:

| Helper | Used by | Bumps `version`? |
|---|---|---|
| `emitTileChanged` | framing writes (pan/zoom/scroll, workspace layout) | No |
| `finishContentEdit` | content writes (text bodies, url/shell freeze) | Yes |

`SetTile` dispatches on `tile.kind` to exactly one of these. Because the
split lives in one place, "framing is not a content edit" — the thing that
makes the descend/ascend round trip idempotent — cannot be violated by
accident. A new mutation physically has to choose a side. This is what
enforced-by-construction looks like; emulate it.

### 4.2 Identity and clone

- Grid/tile/blob ids are SQLite AUTOINCREMENT, never reused. Stored
  references and client caches key on them, so reuse would be catastrophic.
- Clone is an eager deep copy: new ids for the copy, blobs shared by
  content address + refcount, no structural sharing. An edit to one copy
  can never touch another, and no id is ever reassigned. (COW was tried and
  torn out — a fork re-rows tiles, and no patch makes that safe.)
- The storage format is frozen and additive-only; the contract lives in
  `internal/store/CLAUDE.md`. Never delete a DB to absorb a change.
- One layering wrinkle: `internal/store` imports `client/markdown` for
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
responses), so a stale save 409s and reconciles visibly. `cache.Apply`
drops events strictly older than the cached row, and a newer row drops a
clean body while sparing a dirty one — a foreign writer's edit appears, and
unsaved typing survives.

**Events never touch framing.** The SSE path flows only into the cache;
framing writes live only in the gesture/transition code. An event landing
mid-animation updates data and redraws, but cannot move the viewport a
transition is animating. (This separation is verified by inspection only —
see I11.)

**The rendered view is a DOM overlay.** A focused text pane in rendered
mode shows sanitized HTML in a div (`markdown.RenderHTML`: goldmark, go-org
for `.org` names, bluemonday); the editing textarea is another overlay.
Every other view paints raw soft-wrapped source on canvas, wrapped to the
same columns the textarea shows (`markdown.WrapRawText`), so nothing
reflows when focus moves. Grid previews render at constant scale
(`markdown.PreviewWindowFrame` takes only the tile's own facts, so a
sibling pane's width cannot re-wrap a preview — unrepresentable by
signature).

**The bottom bar lives in the focused pane** (`client/wsbar` geometry,
`bottombar.go` glue): workspace crumbs, the anchor block, the descent chain
as clickable square previews (derived per frame from `pane.DescentChain` —
never stored), the centered title, and the circle slot (the + menu / back /
refresh button). Native surfaces on the focused pane carve the band out of
their rects (`panebox.BarInset`), so nothing can occlude it. Clicking a
chain crumb is THE bar ascent gesture; middle-click on a pane is the
in-pane shortcut.

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

**Shell transport.** `shellstreams.ts` dials the sidecar's node export and
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
| `emitTileChanged` / `finishContentEdit` | "does this mutation bump version" | `store/tiles.go` | every store mutation |
| `classifyStoreError` | "what status is this error" | one function | every transport |
| `zoomtrans.LiveFromIntrinsic` / `IntrinsicFromLive` | the viewport transform | one pure pair | preview + descent |
| `client/menu` | "is the menu open, on which pane" | one state machine | every gesture path (was 14 scattered writes) |
| `cache` content entries + `text_flush.go` | "the bytes, their version, and whether they're edited" | one entry per tile id | every save path |
| `shellconn.DecideAutoLive` | "does this descent go live" | one decision | every descent/restore path |
| `localdb.OpenVerified` | the plugin's identity | verify+open+inject fused | every identity read |

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
   implemented in the localdb switch, and again in `conv.go`.
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

- **Boot.** `ListPlugins` returns the plugin list + node identity; panes
  anchor at home (the first plugin's root grid) and "/" is its URL. A
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
- **Ascend.** The intrinsic viewport writes back via `SetTile` (framing —
  no version bump); a url/shell descent freezes its preview (content —
  version bumps); the parent frame pops. A portal ascent writes through the
  containing link tile, or `SetRootView` when there is none. Clicking a bar
  crumb ascends all the way to that level.
- **Drop a tile.** Gesture → `CreateTile`/`PlaceTile`/`CloneTile`,
  id-addressed + version-claimed → server routes → store mutates →
  `Subscribe` event → `cache.Apply` → redraw. Across a plugin boundary
  there is no move: a left-drag creates a LINK in the destination (exit
  well or `link_target_id`), a right-drag CLONES (leaves copy bytes; a
  solid well is refused until deep cross-plugin copy exists). Relocation is
  the explicit two-step: clone, then delete.
- **Open a live URL tile.** The canvas places a rect; IPC asks the native
  layer for a `WebContentsView` on the shared partition; `syncURLViews`
  tracks its bounds every frame and parks it during overlays.
- **Enter a workspace (pane tile).** The third descent verb: flush every
  outer leaf, push a `client/workspace` frame (outer tree + origin), decode
  the layout blob, swap `App.tree`. While inside, a debounced persister
  encodes the live tree, hash-diffs, and posts the layout as a
  `WriteContent` (framing-class — never bumps version) only on change. The
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
