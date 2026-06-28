# Gridwell Architecture

This document is the **true, verified map** of how Gridwell is built: the
layers, the contract between them, the invariants each must hold, and — most
importantly — **where the chronic instability comes from and why**. It is
written for the next engineer (human or agent) who has to change something
without breaking three other things.

`CLAUDE.md` states the *philosophy* (the guiding rule: "things stay as you
left them") and the *domain model* (tiles, wells, clone, plugins). Read it
first. This document states the *machine* — and it was checked against the
code, not the prose. Where the code contradicts a comment, that is called out
explicitly (see [§9 Known drift](#9-known-drift-do-not-trust-these-comments)).

> **The one-sentence diagnosis.** The store and server are sound *by
> construction*. Every fragile invariant lives in the **client/native split**,
> and they all share one root cause: **a single fact is stored in several
> parallel copies with no single owner, and written from many code paths.**
> That is why fixes don't stay fixed — a patch corrects one copy while another
> path keeps writing the rest. The cure is already in the codebase four times
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
│   THE INSTABILITY EPICENTER (10.6k LOC, 0 unit tests — see §4)         │
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
`ssh`/`proxy` plugin forwards the same service. Every byte (live PTY, the
Chromium session blob) crosses this one interface.

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
  authoritative "this well is a link, not owned content" signal. Set by the
  server in `qualifyTiles`; render draws a dashed border from it; delete/clone
  key on it. **This is the cure pattern in miniature: one fact, derived in one
  place, read everywhere.** Emulate it.

---

## 3. Layer: the server — a stateless router (`internal/server`)

**Responsibility.** Hold **no Gridwell state**; route every call to the owning
plugin and translate ids at the boundary.

**How routing works** (`connectHandler`):
1. `route(id)` splits a qualified `<plugin-uuid>/<local-id>`.
2. `localPathFor` strips the prefix going *in*, keeping only the trailing run
   of segments owned by the target plugin.
3. The plugin answers in its own local id space.
4. `qualifyTiles` / `qualifyGrid` / `qualifyEvent` re-qualify ids going *out*.

**The one piece of real logic — and it's exemplary.** `qualifyTiles` sets
`Tile.reference = true` when a `child_grid_id` comes back already-qualified
(i.e. it points into another plugin). The store's delete and clone paths key on
that *same* derived fact, so render, delete, and clone **cannot disagree about
what a link is.** Copy this shape.

**Other consolidations done right:**
- `classifyStoreError` — one home for store-error → HTTP/Connect classification,
  read by both the Connect and raw-HTTP paths.
- `Subscribe` fans in every localdb plugin's event stream into one client stream.

**Known fragility.** `ListPlugins` calls `Info` on every plugin on every
invocation, with no cache or timeout. A slow/hung plugin stalls the launcher.

---

## 4. Layer: plugins & store (`internal/plugin/*`, `internal/store`)

**Spawn model.** `server.yaml` is mandatory (no fallback DB). A plugin entry
with `binary != ""` is spawned as a go-plugin **subprocess** — this is the only
production path. The in-process path survives **solely as a test harness**.
Each plugin owns exactly one SQLite DB and one id space.

- **`localdb`** owns all user content (text, urls, grids) plus shells, sessions,
  and the event stream. It is the only writable plugin.
- **`fs` / `proc`** are read + framing only, over the shared `griddb` helper.
  They map paths/PIDs → stable integers and **`Probe` before any sweep** — a
  failed read must never delete a tile; only a definite `GONE` does.
- **`ssh`** is a transparent `proxy` forwarding the whole service to a remote node.

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
- ⚠️ `internal/store/cow.go` is **misnamed**: COW was removed; the file now holds
  path validation (`checkLeafGrid`) and clone helpers only. See §9.

---

## 5. Layer: the Go→wasm client (`client/wasm`) — the epicenter

**The intended architecture is right:** a thin wasm shim over pure, headlessly
testable `client/*` packages (`pane`, `preview`, `markdown`, `gesture`,
`zoomtrans`, `urlnorm`, …). Those packages are clean and well-tested.

**The problem is the shim never stayed thin.** `client/wasm` is **22 files,
10,660 LOC, and 0 test files.** The hottest files in the entire repository live
here (`input.go` 2,141 LOC, `render.go` 1,187, `right_button.go` 846,
`main.go` 829). `make check` *compiles* this package (the `GOOS=js` build) but
**executes none of it.** Only the e2e harness touches it — as a black box.

### 5.1 The `App` god-object

`App` (`main.go:51`) carries ~30 fields and ~12 maps keyed by pane id, mutated
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

- **The menu has no owner.** There are **14 imperative `a.menuOpen = …`
  assignments** (11 of them `= false`) scattered across `input.go`,
  `right_button.go`, and `file_overlay.go`, plus a `MenuOpen` bool threaded
  through portal frames — and **no `closeMenu()`/`openMenu()` function.** Any
  new gesture-ending path that forgets its own assignment leaves the menu open
  on a stale pane → **"menus disappearing / on the wrong pane."** This is the
  "fix one path, miss another" pattern in its purest form.

- **Text preview can re-wrap.** `markdown.PreviewScaleScroll` has three priority
  sources for scale/scroll. ⚠️ If the framing pane's width ≠ the preview pane's
  width, the markdown **re-flows** rather than showing a scaled copy of what you
  left — a direct "preview is not what I was looking at" vector.

- **Optimistic edits race the SSE event.** `cache.Apply` upserts by id and
  **drops events for grids it hasn't cached.** A local optimistic edit and the
  authoritative `Subscribe` event have **no per-field merge and no version
  interlock**; the conflict refetch is async. Reading (an inbound event during
  an animation) can therefore appear to mutate state — a violation of "reading
  never mutates," enforced only by hope.

---

## 6. Layer: the Electron native shell (`apps/desktop/src/main`) — invisible to `make check`

Live URL pages and live shells are **native `WebContentsView`s** positioned over
the wasm canvas. They are separate webContents off the main page, so **nothing
here is reachable by `make check`** — only `make check-electron` (the live
harnesses under xvfb) and `make check-e2e` (the full app) touch it. This is
exactly why the owner's worst bugs (live tiles, menus over live tiles, previews
of live tiles) escape the fast gate.

**`WebviewRegistry`** owns a `Map<paneId, {view, control, bounds, hidden,
focused, partition, …}>`. Of the 16 files in `src/main`, **only 3 have unit
tests**; `webviews.ts` (444 LOC — the registry, and the documented
bounds/clip/teardown bug source) has none.

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

**Sessions.** One Chromium partition per plugin, `persist:plugin-<uuid>`. ⚠️ Only
cookies are synced via Get/PutSession; localStorage is not. **Teardown** detaches
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

Each one makes a class of bug *unrepresentable*. The fragile areas in §5/§6
need exactly this treatment.

---

## 8. The seam catalog: one fact, many copies

Every entry below is the same disease — a single truth duplicated. Each is a
ranked target for the §7 cure. (Verified against the code, June 2026.)

1. **Viewport / framing** — held in 5 copies (§5.2). *Highest impact: this is
   the descend/ascend round-trip.*
2. **Menu open/closed** — 11 `menuOpen = false` sites + a portal-frame bool, no
   owner (§5.2).
3. **"Controls show only on the focused pane"** — encoded once on the canvas
   (Go) and again as `controlVisible` in the native layer (TS). They can drift.
4. **Native view bounds vs. canvas pane rect** — the per-frame reconciliation in
   `syncURLViews`; coordinate math in two languages, timing-sensitive.
5. **The drag threshold** — `dragThreshold` (Go, `input.go`/`right_button.go`/
   `main.go`) *and* `RIGHT_DRAG_THRESHOLD = 4` (TS, `viewutil.ts`). Same magic
   number, two homes, two languages.
6. **The `SetTile` kind→operation mapping** — described in the proto comment,
   implemented in the `localdb` switch, and again in `conv.go`.
7. **Optimistic local edit vs. authoritative SSE state** — no merge/interlock
   (§5.2).

---

## 9. Known drift (do NOT trust these comments)

Future changes have been mis-led by stale comments. These are wrong; the code is
right:

- `cow.go` and the `Path` proto comment describe a **"COW spine"** that no longer
  exists — clone is an eager copy (§4.2). `cow.go` should be renamed (e.g.
  `clone.go`/`path.go`).
- `loader.go` describes built-in plugins running **in-process**; in production
  every plugin is a separate binary (the in-process path is test-only).
- `config.go` references an **`Attach(config)`** RPC; there is no `Attach` — the
  gRPC connection itself is the only lifecycle (`Info` is the whole handshake).

These are harmless at runtime but actively dangerous to the next change. Fix the
comment in the same commit you touch the file.

---

## 10. Map of the key journeys (for orientation)

- **Descend into a well.** Pane reads the tile's `view_*`, pushes the current
  frame onto `paneStateStack`/`pane.Up`, re-anchors to the child grid, restores
  the stored viewport. *(All five framing copies must move together.)*
- **Ascend.** `saveWellViewBeforeAscent` writes the intrinsic viewport back via
  `SetWellView` (`emitTileChanged` → **no version bump**); the parent frame is
  popped and restored. The round-trip is idempotent **iff** the five copies
  agreed.
- **Drop a tile.** Gesture → `CreateTile`/`MoveTile`/`CloneTile` with the descent
  `Path` → server routes → store mutates → `Subscribe` event → `cache.Apply`
  upserts → redraw.
- **Pan / zoom.** Framing write → `SetTile` (well/text branch) → **no version
  bump** → event fans out → other panes' previews of the same tile update.
- **Open a live URL tile.** Canvas places a rect; IPC asks the native layer for a
  `WebContentsView` on the plugin partition; `syncURLViews` tracks its bounds
  every frame and parks it during overlays.
- **Show the menu.** `menuOpen = true` on the focused pane; native overlays park;
  canvas paints the menu on top. (Closing depends on every gesture-end path
  remembering to clear `menuOpen` — the fragility.)
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
| I7 | **preview = descent target = ascent return** | 5 client copies, sync by convention | ⚠️ **HIGH** |
| I8 | Text preview == what you left (no re-wrap) | `PreviewScaleScroll`, width-dependent | ⚠️ **HIGH** |
| I9 | Controls show only on the focused pane | canvas (Go) + `controlVisible` (TS) | ⚠️ HIGH (dup) |
| I10 | Menu changes only by user action | 11 imperative sites, no owner | ⚠️ **HIGH** |
| I11 | Reading never mutates (SSE during animation; split-pane shared text) | hoped, not enforced | ⚠️ HIGH |

The pattern is unmistakable: **I1–I6 are enforced by construction and don't
break. I7–I11 are enforced by convention and are precisely the owner's recurring
bugs.** Stability means converting the bottom half of this table into the top
half.
