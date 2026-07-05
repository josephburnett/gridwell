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
│   THE INSTABILITY EPICENTER (10.7k LOC, 0 unit tests — see §5)         │
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

**Known fragility.**
- `ListPlugins` calls `Info` on every plugin on every invocation. Each call is
  now timeout-bounded (`pluginInfoTimeout`, degrading to a config-only entry),
  but there is still no cache.
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
  They map paths/PIDs → stable integers. **Their sweep policies diverge — one
  rule, two implementations (a §8-class seam):** `proc` keeps a tile whose
  process is merely unreadable this pass and deletes only on a definite gone
  (`procsource.Exists` consulted before every sweep). `fs` treats an unreadable
  directory as an **empty authoritative listing** (`GetGrid` → `entries = nil`
  → `reconcileTiles` deletes every row). For a genuinely deleted directory that
  is right; for a *transiently* unreadable one (EACCES, an unmounted network
  share) it destroys the user's stored positions and tile ids — when the
  directory returns, tiles re-row with fresh ids and auto-layout positions,
  violating the guiding rule and breaking saved deep links. This is deliberate
  and tested for the vanished-dir case (`TestGetGridUnreadablePathIsEmptyNotError`)
  but conflates "gone" with "can't tell"; the proc policy is the correct one.
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

**The problem is the shim never stayed thin.** `client/wasm` is **22 files,
~10,700 LOC, and 0 test files.** The hottest files in the entire repository
live here (`input.go` ~2,100 LOC, `render.go` ~1,200, `right_button.go` ~850,
`main.go` ~875). `make check` *compiles* this package (the `GOOS=js` build) but
**executes none of it.** Only the e2e harness touches it — as a black box.
Within it, `embed.go`/`embed_drop.go`, `touch.go`, and `file_overlay.go` are
reachable by **no gate at all** (no e2e simulates an embed edit, a touch
gesture, or an OS file drop) — and "embed reverts to link text" is a named
recurring bug with no test home.

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
- **Residual: the optimistic-edit echo (narrow).** A local optimistic edit, then
  the authoritative `Subscribe` echo, both go through `cache.Apply` (upsert by
  id) with no per-field merge or version interlock. For the same content this is
  an idempotent re-apply (no visible change); it only matters under a genuine
  concurrent edit to the same tile — rare in a single-tenant app, but the place a
  reconcile *should* be explicit rather than last-writer-wins.

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
8. **The sweep policy for source-backed tiles** — "never delete on an uncertain
   read" implemented correctly in `proc`, violated in `fs` (§4). One rule, two
   homes, one wrong.
9. **Plugin capabilities** — declared in `Info` (`watch`, `writable`,
   `has_session`) and honored by the server (`413df43` closed the kind-string
   re-derivation). One fact, one derivation; remotes get events + writes.

---

## 9. Known drift (do NOT trust these names)

Stale comments and names have repeatedly misled changes here. The rule: **fix
the comment/name in the same commit you touch the file.** The original COW /
in-process / Attach comment drift is fixed (`cow.go`→`clone.go`,
`cow_test.go`→`clone_test.go`, `config.go`/`loader.go`/`connect_handler.go`
comments corrected). What remains is *naming* drift:

- **"file" vs "text".** Go identifiers standardized on `Text*`
  (`TextFocus`/`TextMode`), but the wasm layer still says `fileTextarea`,
  `fileInnerBox`, `startFileDescent`, and `pane.Pane`'s JSON tags are the
  legacy `file_focus`/`file_mode`. The rename is half-done; finish it when
  touching those files.
- **Three JSON vocabularies for one shape.** `pane.Pane` (`file_focus`),
  `pane.Frame` (`tf`/`tm`), and `panestate.Saved` (`text_focus`) serialize the
  same conceptual fields under three key schemes. Harmless while they never
  cross-decode; a hazard the moment one does.
- **`panebox.OvertakeZoom` is misnamed** — it computes `zoomtrans.Fit` (min
  ratio), not `Overtake` (max); callers carry apology comments.
- **`urlStreams`-era naming**: the live URL handle is a webview bridge, not a
  stream (noted in the stabilization plan's parking lot).

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
| I11 | Reading never mutates (SSE during animation) | events flow only to `cache`; framing writes only in input/urlsync — separation verified **by code inspection only**. No mid-transition event-injection test exists, and the optimistic-echo reconcile (last-writer-wins, no version interlock) is untested at any level. | ⚠️ inspected, **untested** |
| I12 | A plugin's user state survives its source being unreachable | `proc`: Probe-before-sweep ✅. `fs`: **violated** — an unreadable dir sweeps rows (§4) | ⚠️ **open bug (fs)** |

Progress this effort converted most of the bottom half toward the top: **I8/I10
construction-enforced and tested; I7/I9 verified and locked/tested.** I8 was
incorrectly marked "mostly construction" before fix #35 — a cross-pane
reach-through (`paneFocusedOnFile`) in the preview path was the real residual;
it is now removed and locked by two new tests. The genuine-convention items
left: **I7** — the five framing copies are five legitimate *roles* kept
consistent by convention (round trip tested by `framing-roundtrip.spec.ts`; a
deeper single-owner `Frame` is possible but not warranted without a visual/render
net) — and **I11**, whose separation is real in today's code but guarded by
nothing; a new write into the SSE path would regress it silently. I12 is the
newest entry: the sweep-policy rule the docs asserted turns out to hold in only
one of the two plugins it governs.
