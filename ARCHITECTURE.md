# Gridwell Architecture

This document is the **true, verified map** of how Gridwell is built: the
layers, the contract between them, the invariants each must hold, and — most
importantly — **where the chronic instability comes from and why**. It is
written for the next engineer (human or agent) who has to change something
without breaking three other things.

`README.md` states what Gridwell *is* — the principle ("things stay as you
leave them"), the primitives, and the gesture vocabulary. `CLAUDE.md` states
the *philosophy* in full and the *engineering charter* (how to change the
code without breaking it). Read them first. This document states the *machine* — and it was checked against the
code, not the prose. Where the code contradicts a comment, that is called out
explicitly (see [§9 Known drift](#9-known-drift-do-not-trust-these-comments)).

> **The one-sentence diagnosis.** The store and server are sound *by
> construction*. Every fragile invariant lives in the **client/native split**,
> and they all share one root cause: **a single fact is stored in several
> parallel copies with no single owner, and written from many code paths.**
> That is why fixes don't stay fixed — a patch corrects one copy while another
> path keeps writing the rest. The cure is already in the codebase five times
> over (§7). Apply it everywhere the [§8 seam catalog](#8-the-seam-catalog-one-fact-many-copies) lists.

---

## 1. The system in one breath

```
┌──────────────────────────────────────────────────────────────────────┐
│ Electron main process        apps/desktop/src/main/                    │
│   native WebContentsViews positioned over a wasm canvas                │
│   (live URL pages + live shells). `make check` CANNOT SEE THIS LAYER.  │
└───────────────┬──────────────────────────────────────────────────────┘
                │  IPC  (window.gridwell bridge, CSS-px bounds both ways)
┌───────────────▼──────────────────────────────────────────────────────┐
│ Go→wasm client               client/wasm/  +  pure client/* packages  │
│   canvas, panes, gestures, framing, previews, menu, embeds            │
│   THE INSTABILITY EPICENTER (~13.4k LOC, 0 unit tests — see §5)        │
└───────────────┬──────────────────────────────────────────────────────┘
                │  Connect-RPC  (the Gridwell service)
┌───────────────▼──────────────────────────────────────────────────────┐
│ Local server                 internal/server, internal/rpc            │
│   STATELESS router: splits <uuid>/<id>, forwards, re-qualifies        │
└───────────────┬──────────────────────────────────────────────────────┘
                │  go-plugin gRPC  (the SAME Gridwell service)
┌───────────────▼──────────────────────────────────────────────────────┐
│ Plugins        internal/plugin/{localdb,fs,proc,ssh,proxy}            │
│   each is a separate binary owning one SQLite DB + one id space        │
└───────────────┬──────────────────────────────────────────────────────┘
                │
┌───────────────▼──────────────────────────────────────────────────────┐
│ Store                        internal/store/                          │
│   ids, the framing-vs-content version split, clone, blobs, sessions   │
│   SOLID BY CONSTRUCTION — the model to emulate                        │
└──────────────────────────────────────────────────────────────────────┘
```

**One contract, every hop.** `api/gridwell/v1/data.proto` defines a single
gRPC service, `Gridwell`, implemented identically by the local server, every
plugin, and a remote node reached over SSH. "Remote" adds no vocabulary — the
`ssh` plugin is a transparent `proxy` of a remote node's **export**
(`nodeexport.go`: the same service over raw gRPC, routed by the qualified ids
each request carries). Ids **chain** one segment per hop
(`<ssh>/<plugin>/<id>`), so any depth of mounting routes generically. Every
byte (live PTY, the Chromium session blob) crosses this one interface.

**Every node exposes a node grid.** A node's plugin list is a real, read-only
grid — `<node_id>/0`, served by `internal/server/nodegrid.go` — one dashed
link tile per plugin. It is the FEDERATION surface: descending into an ssh
mount lands on the remote's node grid, and `?a=<node>/0` addresses it locally.
It is NOT the local landing page (owner decision 2026-07-19, reversing the
launcher decision of PR #41): the client boots into **home** — the first
configured plugin's root grid (`rpc.HomeGrid`; node grid as fallback) — and
plugins are reached from the **+ menu's top row** (click = portal descent,
drag = drop an exit-well link).

---

## 2. Layer: the proto contract (`api/gridwell/v1/data.proto`)

**Responsibility.** Single source of truth for *both* the wire types and the
persisted record shapes. Every field on `Grid`/`Tile` maps 1:1 to a column in
`internal/store/schema.go`; a drift-lint test fails the build if they diverge.

**The surface** (one method per *concept*, not per *kind*):

| Group | Methods |
|---|---|
| Lifecycle | `Info`, `Probe`, `ListPlugins` |
| Reads | `GetGrid`, `GetTile`, `GetTileContent`, `GetTilePreview` |
| Mutations | `CreateTile`, `SetTile`, `MoveTile`, `CloneTile`, `ResizeTile`, `UpdateText`, `DeleteTile`, `SetTileAlt`, `Mount` |
| Live bytes | `OpenShell` (PTY both ways), `GetSession`/`PutSession`, `ShellSessionAlive` |
| Events | `Subscribe` |

**The two fields that encode the guiding rule:**

- `Tile.view_x / view_y / view_zoom` — documented in the proto as *"at once the
  preview frame, the descent target, and the ascent return value."* One value,
  three readings. This is face #3 of the guiding rule, in the schema.
- `Tile.reference` (bool, **server-derived, never stored**) — the single
  authoritative "this well is a link, not owned content" signal. Derived once
  per fact-owner: `qualifyTiles` derives it for a leaf plugin's tiles; for a
  TRANSIT plugin (a node mount) the remote node already derived it, and
  `qualifyTilesTransit` passes the wire bit through verbatim (a remote
  plugin's interior well stays solid). Render draws a dashed border from it;
  delete/clone key on it; descent PORTALS through it (anchor swap). **This is
  the cure pattern in miniature: one fact, derived in one place, read
  everywhere.** Emulate it.
- `Grid.writable` (bool, wire-only) — the owning plugin's per-grid mutation
  capability, stamped by the serving node (from Info for leaves, passed
  through for transit). The client's "+ palette shows here" gate reads the
  grid, never a local plugin-list lookup, because one ssh mount fronts many
  remote plugins with differing capabilities.

---

## 3. Layer: the server — a stateless router (`internal/server`)

**Responsibility.** Hold **no Gridwell state**; route every call to the owning
plugin and translate ids at the boundary.

**How routing works** (`connectHandler`, and identically the gRPC node export):
1. `route(id)` peels the FIRST segment of a qualified id; the remainder passes
   through verbatim — so `<ssh>/<plugin>/<id>` chains resolve one hop at a
   time, at any depth. The node's own uuid routes to the in-process node-grid
   provider (`routeClient`).
2. `localPathFor` strips the prefix going *in*, keeping only the trailing run
   of segments owned by the target plugin.
3. The plugin answers in its own id space — bare ints for a leaf, chains
   (the remote's qualified view) for a transit plugin.
4. `qualifyTiles` / `qualifyGrid` / `qualifyEvent` re-qualify ids going *out*;
   `qualifyTilesFor` picks the leaf rule (qualify + derive `reference`) or the
   transit rule (prepend one segment everywhere, trust the wire bits) from
   `Registry.Transit` — a config-time fact about the local transport binary,
   deliberately not an Info capability so it holds while the remote is down.

**Two wire surfaces, one implementation.** The Connect handler serves
browsers; `NodeHandler` wraps the same mux in h2c and routes raw gRPC to the
**node export**, whose unary methods delegate straight to the connectHandler
(`statusErr` maps Connect codes back to gRPC) and whose plugin-grade streams
(`OpenShell`, `GetSession`, `PutSession`) route by the id in their first
message. `Subscribe` shares the one fan-in body (`subscribe`). A remote
mounter and a browser literally exercise the same routing code.

**The one piece of real logic — and it's exemplary.** `qualifyTiles` sets
`Tile.reference = true` when a `child_grid_id` comes back already-qualified
(i.e. it points into another plugin). The store's delete and clone paths key on
that *same* derived fact, so render, delete, and clone **cannot disagree about
what a link is.** Copy this shape.

**Other consolidations done right:**
- `classifyStoreError` — one home for store-error → HTTP/Connect classification,
  read by both the Connect and raw-HTTP paths.
- `Subscribe` fans in every localdb plugin's event stream into one client stream.

**Known fragility.**
- `Info` handshakes are timeout-bounded (`pluginInfoTimeout`, degrading to a
  config-only entry) and cached per-uuid after first success (`infoCache`,
  invalidated on `SetRootView` because root_view_* ride the handshake).
- ~~The server dispatched two capabilities on the kind *string* instead of the
  `Info` handshake~~ — **fixed** (`413df43`): `Subscribe` fan-in gates on
  `Info.watch` and `buildPluginInfo` reads `Info.writable`, so a remote
  localdb over ssh gets events and is writable. Capabilities are facts the
  plugin declares once in `Info`, never re-derived from the kind string.

---

## 4. Layer: plugins & store (`internal/plugin/*`, `internal/store`)

**Spawn model.** `server.yaml` is mandatory (no fallback DB). A plugin entry
with `binary != ""` is spawned as a go-plugin **subprocess** — this is the only
production path. The in-process path survives **solely as a test harness**.
Each plugin owns exactly one SQLite DB and one id space.

- **`localdb`** owns all user content (text, urls, grids) plus shells, sessions,
  and the event stream. It is the only writable plugin.
- **`fs` / `proc`** are read + framing only, over the shared `griddb` helper.
  They map paths/PIDs → stable integers. **Both enforce the one sweep rule:**
  a failed read never deletes a tile row — only a *definite gone* does. `proc`
  consults `procsource.Exists` before every sweep; `fs` (fixed in `cce8614`)
  sweeps only on `ErrNotExist` and, for any other read error (EACCES, an
  unmounted network share), skips the reconcile and serves the stored rows
  verbatim, so positions and ids survive until the source is readable again.
  Tested from both sides (`fs_stability_test.go` via the `SetReadDir` seam:
  permission error keeps rows, ENOENT sweeps).
- **`ssh`** is a transparent `proxy` of a whole remote node: it dials the
  remote's export through the tunnel (`internal/plugin/sshdial`, seam-tested
  against a real in-process sshd) and forwards the full service verbatim. Its
  Info is the remote node's (root = the remote node grid), and the host marks
  it TRANSIT so response ids gain exactly one segment per hop.

### 4.1 The best-enforced invariant in the codebase: framing ≠ content

This is the model every other invariant should be rebuilt to match.

In `internal/store/tiles.go`, **two named helpers** split every mutation:

| Helper | Used by | Bumps `version`? |
|---|---|---|
| `emitTileChanged` | `SetWellView`, `SetTextView` (framing: pan/zoom/scroll) | **No** |
| `finishContentEdit` | `SetURLState`, `SetShellPreview`, `UpdateText` (content) | **Yes** |

`localdb.SetTile` dispatches on `tile.kind` to exactly one of these. Because the
split lives in **one place**, "framing is not a content edit" (face #3 of the
guiding rule, the thing that makes the descend/ascend round-trip idempotent)
**cannot be violated by accident.** A new mutation physically has to choose a
side. *This is what "enforced by construction" looks like.*

### 4.2 Identity & clone (sound)

- Grid/tile/blob ids are SQLite `AUTOINCREMENT`, **never reused**. Stored
  references (embeds, deep links, client caches) are keyed by id, so reuse would
  be catastrophic — hence never.
- Clone is an **eager deep copy** (not copy-on-write): new ids for the copy,
  blobs shared by content-address + refcount, no structural sharing. An edit to
  one copy can therefore never touch another, and **no id is ever reassigned.**
- One layering wrinkle: `internal/store` imports `client/markdown` for
  `AltFromSource` (deriving a text tile's label from its body). The function is
  pure and shared, but the dependency arrow points from the persistence layer
  into the client tree — if it grows, move the shared derivation into a neutral
  package rather than deepening the inversion.

---

## 5. Layer: the Go→wasm client (`client/wasm`) — the epicenter

**The intended architecture is right:** a thin wasm shim over pure, headlessly
testable `client/*` packages (`pane`, `preview`, `markdown`, `gesture`,
`zoomtrans`, `urlnorm`, …). Those packages are clean and well-tested.

**The problem is the shim never stayed thin.** `client/wasm` is **26 files,
~13,400 LOC, and 0 test files** (as of July 2026 — and still growing). The
hottest files in the entire repository live here (`input.go` ~2,300 LOC,
`render.go` ~1,350, `right_button.go` ~900, `main.go` ~1,150). `make check` *compiles* this package (the `GOOS=js` build) but
**executes none of it.** Only the e2e harness touches it — as a black box.
Within it, `embed.go`/`embed_drop.go`, `touch.go`, and `file_overlay.go` are
reachable by **no gate at all** (no e2e simulates an embed edit, a touch
gesture, or an OS file drop) — and "embed reverts to link text" is a named
recurring bug with no test home.

### 5.1 The `App` god-object

`App` (`main.go:58`) carries ~30 fields and ~12 maps keyed by pane id, mutated
from many synchronous gesture paths *and* from goroutines (SSE, shell/url
streams) with **no transaction and no single owner.** Two structural
consequences:

- **Per-pane maps orphan state.** When a pane is closed and reopened, entries
  keyed by the old pane id leak or go stale. This is the mechanical root of a
  whole class of "it worked, then it didn't" bugs.
- **No serialization point.** A gesture handler and an SSE callback can both
  mutate the same field; there is no place that says "this is the one writer."

### 5.2 The fragile facts (each held in several copies — this is the disease)

- **Viewport / framing lives in FIVE places** that must agree for the
  descend/ascend round-trip to be idempotent:
  1. `pane.Pane.{Anchor, Path, Cx, Cy, Zoom}` (the live pane)
  2. `App.paneStateStack` (the ascent-parent stack)
  3. the server tile's `view_x/view_y/view_zoom`
  4. `pane.Up` portal frames
  5. the URL bar / bookmark path

  They desync on pane close and when an SSE event lands mid-animation →
  **"preview goes wonky."** The well-preview math itself (`render.go` drawing the
  stored `view_*` small via `zoomtrans`, `saveWellViewBeforeAscent` writing back
  an intrinsic window-independent `ViewZoom`) is *well-designed*; the fragility
  is **purely the cross-copy sync**, not the geometry.

- **The menu — CURED (the pattern's proof).** It used to be 14 imperative
  `a.menuOpen = …` assignments scattered across three files with no owner —
  the "fix one path, miss another" pattern in its purest form. It is now the
  single-owner `client/menu` state machine (`Open`/`Close`/focus rules), unit-
  and e2e-tested (`menu-focus.spec.ts`). Kept here as the worked example: this
  is what applying §7 to a seam looks like, and what the remaining seams need.

- **Text preview re-wrap (I8) — FIXED (#35, was incorrectly "verified handled").**
  The earlier analysis missed a cross-pane reach-through: `drawMarkdownNode`
  called `paneFocusedOnFile` to find any sibling pane descended into the same
  tile, then fed that pane's live `innerW` as `focused=true` to
  `PreviewScaleScroll`. In a split-pane setup this caused two bugs:
  (A) the preview in pane B was laid out at pane A's live inner width, not the
  stored framing — wrong size every time focus changed; and (B) `hideForTextarea`
  in the preview path suppressed canvas paint for every preview of the tile when
  another pane was editing it in text mode — blank preview.
  `TestPreviewContentWidthInvariantToFootprint` only varied the *footprint*; it
  never varied the *focused pane width* that `paneFocusedOnFile` was returning —
  so the gap was invisible to that test.
  Fix: `drawMarkdownNode` always passes `focused=false` (stored framing, per the
  guiding rule: preview = descent target = ascent return); `paneFocusedOnFile` is
  removed. `textedit.CanvasHiddenByOverlay` is the single-owner predicate for
  "canvas paints vs overlay covers" — it is never called in the preview path and
  requires `textareaReady=true` in the descended path (prevents the loading-race
  blank). Locked by `TestPreviewNotAffectedByFocusedPaneWidth` and
  `text-pane-split.spec.ts`.

- **SSE during animation (I11) — framing is safe by construction; only data
  fans out.** Verified: every write to pane framing (`Cx/Cy/Zoom/Path/Anchor`)
  lives in `urlsync.go` (URL/nav) or `input.go` (gestures + the transition
  system); the SSE path (`startSSE` → `cache.Apply` + `fetchGrid`) and the
  `cache` package contain **none**. So an event mid-transition updates tile/grid
  *data* and redraws, but cannot move the framing a transition is animating —
  it's correct fan-out ("mutation is local and reflected"), not a "reading
  mutates" bug. The animation owns framing; events own data; the two never cross.
- **The echo/foreign-writer reconcile is now explicit (was: last-writer-wins).**
  `cache.Apply` drops an event STRICTLY OLDER than the cached row (the stale-echo
  interlock, `TestApplyStaleEchoDropped`), and both row-arrival paths (`Apply`
  and `PutGrid`, one `reconcileContent`) age the text-body cache: a newer row
  drops a CLEAN body (so a foreign writer's edit refetches and appears) and
  spares a DIRTY one (unsaved typing). Content entries bind `{bytes, base
  version, dirty}` — one owner — and saves claim `SaveBasis` (the version the
  bytes derive from, advanced only by fetches and save responses), so a client
  can never claim a version whose content it hasn't seen; a stale save 409s and
  reconciles visibly. The clean textarea buffer follows the entry
  (`DecideTextareaSync`), so no stale DOM copy survives to be flushed back.
  Crossed end-to-end by `foreign-writer.spec.ts` (a second writer against the
  live app) and, for the remote transport, the federation gate's event step.
- **Text content has ONE door to the server, and it never reads the DOM
  (2026-07-18).** The content entry also owns the pending-edit fact: every
  keystroke (raw mode mirrors on input; rendered mode always did) lands in the
  tile-scoped entry, and every flush — debounce sweep, ascent, pane collapse,
  workspace boundary, mode toggle — goes through `client/wasm/text_flush.go`,
  which posts `cache.DirtyContent(tileID)` iff dirty. The prior design read the
  singleton `<textarea>` at flush time and paired it with whatever tile the
  flushed pane pointed at; a bulk flush over a pane the singleton wasn't bound
  to saved one document's bytes as another's content, at the victim's own valid
  basis (the cross-tile stomp — the incident that destroyed a remote tile's
  content). Guards at individual call sites (`ShouldDebouncedSaveFire`,
  `FlushOldFirst`) were deleted WITH the hazard they policed: bytes can only be
  posted under the id they were edited under, and a clean entry never writes
  (a mere open/close no longer bumps the version). Crossed end-to-end by
  `cross-tile-stomp.spec.ts`, `stranded-edit.spec.ts`, and
  `workspace-rebind.spec.ts`.

---

## 6. Layer: the Electron native shell (`apps/desktop/src/main`) — invisible to `make check`

Live URL pages and live shells are **native `WebContentsView`s** positioned over
the wasm canvas. They are separate webContents off the main page, so **nothing
here is reachable by `make check`** — only `make check-electron` (the live
harnesses under xvfb) and `make check-e2e` (the full app) touch it. This is
exactly why the owner's worst bugs (live tiles, menus over live tiles, previews
of live tiles) escape the fast gate.

**`WebviewRegistry`** owns a `Map<paneId, {view, control, bounds, hidden,
focused, partition, …}>`. Of the 15 implementation files in `src/main`, 8
now have unit tests — but `webviews.ts` (823 LOC — the registry, and the
documented bounds/clip/teardown bug source) is still not one of them.

**The tightest timing seam in the system — `syncURLViews`, run every frame:**
- The renderer sends CSS-px `ContentBox` bounds over IPC; `roundBounds` snaps to
  integer DIP with a 1px floor; `boundsEqual` skips no-op churn.
- `liveOverlaysHidden = dragging || rightDrag || leftResize || menuOpen` **parks**
  native views so canvas overlays (the menu, drag ghosts, resize bands) can paint
  *on top* of them. Get this predicate wrong and a live view either eats input it
  shouldn't or vanishes when it shouldn't.
- `controlVisible = !hidden && focused` is the **native mirror** of the canvas
  rule "controls show only on the focused pane." The same rule, expressed twice,
  in two languages — see §8.

**Sessions.** One Chromium partition per plugin, `persist:plugin-<uuid>`
(behind a mount the key is the full namespace chain, peeled per hop — PR #68).
The Get/PutSession blob is a **v2 envelope** `{v:2, cookies, files}`: cookies
plus the partition's on-disk session state under the `SESSION_STATE_ROOTS`
allowlist (Local Storage, Session Storage, IndexedDB, WebStorage — never the
Chromium caches, the 238MB lesson of #123). Restore hydrates **only a
never-initialized partition** (dir-existence check) so a stale blob can never
roll live logins back (#120's root cause). **Teardown** detaches
both the view and its control even if the preview capture times out (a hard-won
fix — capture-independent teardown).

---

## 7. The cure pattern (already in the codebase — copy it)

The fix for the whole instability class is one discipline: **derive a fact in
exactly one place and read it everywhere; never store the same truth twice.**
The codebase already does this correctly in four places. These are the
templates:

| Exemplar | The one fact | Where derived | Read by |
|---|---|---|---|
| `Tile.reference` | "this well is a link" | `server.qualifyTiles` | render, delete, clone |
| `classifyStoreError` | "what HTTP status is this error" | one function | Connect + raw-HTTP paths |
| `emitTileChanged` / `finishContentEdit` | "does this mutation bump version" | `store/tiles.go` | every store mutation |
| `zoomtrans.LiveFromIntrinsic` / `IntrinsicFromLive` | "the viewport transform" | one pair of pure fns | preview + descent |
| `client/menu` | "is the menu open, and on which pane" | one state machine | every gesture path (was 14 scattered writes) |

Each one makes a class of bug *unrepresentable*. The fragile areas in §5/§6
need exactly this treatment.

---

## 8. The seam catalog: one fact, many copies

Every entry below is the same disease — a single truth duplicated. Each is a
ranked target for the §7 cure. (Verified against the code, June 2026.)

1. **Viewport / framing** — five *roles* kept consistent by convention (§5.2);
   the round trip is locked by `framing-roundtrip.spec.ts`. *Highest impact:
   this is the descend/ascend round-trip.*
2. ~~**Menu open/closed**~~ — **CURED**: single owner `client/menu` (§5.2).
3. **"Controls show only on the focused pane"** — encoded once on the canvas
   (Go) and again as `controlVisible` in the native layer (TS). Data is
   single-sourced (wasm feeds focus) and the propagation is e2e-tested
   (`control-focus.spec.ts`); the predicate still exists twice.
4. **Native view bounds vs. canvas pane rect** — the per-frame reconciliation in
   `syncURLViews`; coordinate math in two languages, timing-sensitive. (The
   pure math is extracted and tested in `viewutil.ts`.)
5. **The drag threshold** — `dragThreshold` (Go, the declared owner in
   `main.go`) plus two forced copies (TS `viewutil.ts`, the sandboxed
   `urlview-preload.ts`). Drift-linted by `gesture-threshold.test.ts`.
6. **The `SetTile` kind→operation mapping** — described in the proto comment,
   implemented in the `localdb` switch, and again in `conv.go`.
7. **Optimistic local edit vs. authoritative SSE state** — no merge/interlock
   (§5.2). The one framing-class residual with **no test at any level**.
8. ~~**The sweep policy for source-backed tiles**~~ — **CURED** (`cce8614`):
   "never delete on an uncertain read" now holds in both `proc` and `fs`
   (§4), each tested.
9. **Plugin capabilities** — declared in `Info` (`watch`, `writable`,
   `has_session`) and honored by the server (`413df43` closed the kind-string
   re-derivation). One fact, one derivation; remotes get events + writes.

---

## 9. Known drift (do NOT trust these names)

Stale comments and names have repeatedly misled changes here. The rule: **fix
the comment/name in the same commit you touch the file.** Most of the
original catalog is resolved: the COW / in-process / Attach comment drift
(`cow.go`→`clone.go`, corrected loader/handler comments), the file→text Go
identifier sweep and the `tf`/`tm` Frame keys (PR #74), the
`panebox.OvertakeZoom`→`FitZoom` rename, and the `urlStreams`-era naming are
all fixed. What remains (verified July 2026):

- **Three legacy JSON keys on `pane.Pane`.** `TextScrollX/Y` and `TextZoom`
  still tag as `file_scroll_x`/`file_scroll_y`/`file_zoom` while every
  neighboring field (and `pane.Frame`, `panestate.Saved`, and the persisted
  `LayoutV1` codec in `client/pane/wire.go`) says `text_*`. Inert today —
  nothing marshals `pane.Pane` itself — but a hazard the moment something
  does; finish the rename when touching that struct.

---

## 10. Map of the key journeys (for orientation)

- **Boot.** `ListPlugins` returns the plugin list + node identity; panes
  anchor at **home** — the first configured plugin's root grid
  (`rpc.HomeGrid`, node grid as fallback), a fetched grid like any other —
  and "/" is its URL.
- **Descend into a well.** Pane reads the tile's `view_*`, pushes the current
  state onto `paneStateStack`, appends to `Path`, restores the stored viewport.
  *(All five framing copies must move together.)* A **link tile**
  (`Tile.reference` — a node-grid plugin tile, a mounted well, a cross-plugin
  clone) descends as a **PORTAL** instead: push a `pane.Up` frame, swap the
  Anchor to the link's target — so every path id and the URL stay within one
  anchor's namespace at every depth.
- **Ascend.** `saveWellViewBeforeAscent` writes the intrinsic viewport back via
  `SetWellView` (`emitTileChanged` → **no version bump**); the parent frame is
  popped and restored. A portal ascent does the same through the containing
  link tile (`portalWellForFrame`; on a node-grid tile the provider maps the
  write onto the plugin's own `SetRootView`). A **+ menu portal** has no
  containing tile: `savePluginRootViewBeforeAscent` writes the plugin's root
  view directly via `SetRootView` — same fact, no tile carrier. The
  round-trip is idempotent **iff** the copies agreed.
- **Drop a tile.** Gesture → `CreateTile`/`MoveTile`/`CloneTile` with the descent
  `Path` → server routes → store mutates → `Subscribe` event → `cache.Apply`
  upserts → redraw. A right-drag across a plugin boundary becomes a LINK
  (`cloneAcrossPlugins`: exit well sharing the source grid) or a byte copy
  (leaves); a left-drag move never crosses an id namespace (rejected at
  `DecideDrop`).
- **Pan / zoom.** Framing write → `SetTile` (well/text branch) → **no version
  bump** → event fans out → other panes' previews of the same tile update.
- **Open a live URL tile.** Canvas places a rect; IPC asks the native layer for a
  `WebContentsView` on the plugin partition; `syncURLViews` tracks its bounds
  every frame and parks it during overlays.
- **Enter a workspace (pane tile).** The THIRD descent verb: flush every
  outer leaf (the collapse path's flush — text saves, url/shell freeze,
  forgetPane), push a `client/workspace` frame (outer tree + origin pane +
  tile id/version), decode the layout blob (`client/pane` LayoutV1; ids are
  owner-frame-relative, the reader prepends its transit chain), swap
  `App.tree`. The bar (`client/wsbar`, reserved band like the notice strip)
  names the nesting and owns the way out: crumb k leaves workspace k and
  deeper. While inside, `draw()` arms a debounced persister that encodes the
  live tree, hash-diffs, and posts `SetPaneLayout` (framing — never bumps
  version) only on change; the URL is `?w=<tile id>` and nothing else.
- **Show the menu.** `menu.Open(paneID)` on the focused pane (the one owner —
  `client/menu`); native overlays park; canvas paints the menu on top. Closing
  goes through the same owner's transitions (focus change, ascent, gesture end).
- **Render an embed.** `ResolveEmbedTileID` → `fetchTileByID` →
  `PlanEmbedDescent`, possibly re-anchoring across a plugin boundary.

---

## 11. The invariant inventory

Every invariant the system must hold, with **where it is enforced** and a
**fragility flag**. Construction-enforced invariants are safe to build on;
convention-only invariants are where bugs are born — they need the §7 cure.

| # | Invariant | Enforced where | Fragility |
|---|---|---|---|
| I1 | Ids never reused | SQLite AUTOINCREMENT | ✅ construction |
| I2 | Framing write ≠ content edit (no version bump) | `emitTileChanged`/`finishContentEdit` | ✅ construction |
| I3 | Clone is an eager deep copy; no id reassigned | `CloneTile` | ✅ construction |
| I4 | Blobs immutable, content-addressed, refcounted | store blob layer | ✅ construction |
| I5 | "Is a link" is one derived fact | `qualifyTiles` → `Tile.reference` | ✅ construction |
| I6 | Qualified-id routing (`<uuid>/<id>`) | server `route`/`localPathFor` | ✅ construction |
| I7 | **preview = descent target = ascent return** | 5 client copies synced by convention, but the round trip is now locked by `framing-roundtrip.spec.ts` | ⚠️ convention, **tested** |
| I8 | Text preview == what you left (no re-wrap) | `PreviewScaleScroll` lays out at the framing `ContentW` + scales; `TextW` = the descent wrap width. `drawMarkdownNode` always passes `focused=false` (stored framing only). Tested: `TestPreviewContentWidthInvariantToFootprint`, `TestPreviewNotAffectedByFocusedPaneWidth`, `text-pane-split.spec.ts` | ✅ construction + tested (fix #35) |
| I9 | Controls show only on the focused pane | wasm owns focus → native `controlVisible` (unit-tested); the wasm→native propagation is now e2e-tested (`control-focus.spec.ts`) | ✅ data single-sourced + tested (predicate dup remains) |
| I10 | Menu changes only by user action | one owner `client/menu` (was 11 imperative sites); unit + e2e tested | ✅ construction |
| I11 | Reading never mutates (SSE during animation) | events flow only to `cache`; framing writes only in input/urlsync — separation verified **by code inspection only**; no mid-transition event-injection test exists. The echo/foreign-writer reconcile HAS graduated: stale echoes dropped, content aged by `reconcileContent`, saves claim `SaveBasis` — unit-tested (`client/cache`) and crossed by `foreign-writer.spec.ts` + the federation event step. | ⚠️ framing separation inspected-only; reconcile ✅ construction + tested |
| I12 | A plugin's user state survives its source being unreachable | `proc`: Probe-before-sweep; `fs`: ENOENT-only sweep (`cce8614`), both tested (§4) | ✅ construction + tested |
| I13 | A workspace (pane tile) restores exactly as left; a pure visit never writes | the live tree is the ONE in-session owner; the blob is the at-rest form, DERIVED by the persister (encode + hash-diff — no per-gesture hooks to forget; identical bytes = no write). Codec round-trip property + golden v1 fixture (`client/pane`), persister decision unit-tested (`client/workspace`), round trip + reload + read-only-blob e2e (`workspace-*.spec.ts`) | ✅ construction + tested |

Progress this effort converted most of the bottom half toward the top: **I8/I10
construction-enforced and tested; I7/I9 verified and locked/tested.** I8 was
incorrectly marked "mostly construction" before fix #35 — a cross-pane
reach-through (`paneFocusedOnFile`) in the preview path was the real residual;
it is now removed and locked by two new tests. The genuine-convention items
left: **I7** — the five framing copies are five legitimate *roles* kept
consistent by convention (round trip tested by `framing-roundtrip.spec.ts`; a
deeper single-owner `Frame` is possible but not warranted without a visual/render
net) — and **I11**, whose FRAMING separation is real in today's code but guarded
by nothing; a new framing write into the SSE path would regress it silently.
(I11's other half — the echo/foreign-writer reconcile — graduated: version
interlock + content aging in `client/cache`, crossed by `foreign-writer.spec.ts`.)
I12 graduated (`cce8614`): the sweep rule now holds, tested, in both plugins
it governs.
