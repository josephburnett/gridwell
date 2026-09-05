# W1 — navigation orchestration as a pure machine

`client/nav` owns navigation as data. A gesture plus a world snapshot goes in;
an ordered effect list comes out. `client/wasm` gathers the snapshot, executes
the effects, and feeds async answers back. No decision stays in the shim.

The machine reads `pane.Stack` and projects it. It never stores a second copy
of where a pane is. The one fact it owns is the set of suspended
continuations: which async answers are still owed, and what each one still
needs to be true.

## 1. The effect vocabulary

Thirty-one effects, derived from what `nav.go`, `input.go`, `urlsync.go` and
`workspace.go` do today. Nothing here is new behavior.

**Place and tree** (6)

| Effect | Payload | Executor today |
|---|---|---|
| `InstallPlace` | PaneID, Stack, Viewport *{Cx,Cy,Zoom} | `fp.Stack = …`, `p.Reset`, the ascent's instant landing |
| `ClearSelection` | PaneID | `a.clearSelected` |
| `RelocatePane` | PaneID, DestPaneID, TileID, Foot, Zoom | `op.RelocateTo` in `finishPromote` |
| `ForgetPane` | PaneID | `a.forgetPane` |
| `InstallLevel` | Level pane.Level, Tree *pane.Tree, Baseline []byte, KeepOuter bool | `a.installWorkspace` |
| `PopLevel` | Animate bool, OriginPane string | the body of `ascendLevels`' loop |

**Writeback** (6)

| Effect | Payload | Executor today |
|---|---|---|
| `FlushFraming` | — | `a.flushFramingSave` (the `pane.FramingWriters` rule stays in the executor) |
| `PersistFraming` | PaneID, Owner pane.FramingOwner | `a.persistFraming` |
| `SaveText` | PaneID, TileID | `a.saveTextBeforeAscent` |
| `FlushDirtyText` | — | `a.flushDirtyText` |
| `FlushLayout` | — | `a.flushWorkspaceSave` |
| `FlushDroppedSubtree` | — | `a.flushDroppedSubtree(a.tree.Root)` |

**Transition** (2)

| Effect | Payload |
|---|---|
| `CancelTransition` | PaneID (`""` = all) |
| `StartTransition` | PaneID, Segments []transition.Segment, TraceTileID, Land Token |

`Land` is the continuation the executor resumes from the transition's
`OnComplete`. A cancelled transition still lands, so the resume happens either
way — that contract stays in `client/transition`.

**Surface** (4)

| Effect | Payload |
|---|---|
| `CloseStream` | PaneID, Kinds (URL/Shell/Both), Freeze bool, FreezeOnto *{TileID,GridID} |
| `OpenStream` | PaneID, TileID, Kind |
| `PlaceURLView` | PaneID, TileID |
| `RefreshOverlay` | — |

**Cache and await** (4)

| Effect | Payload |
|---|---|
| `FetchGrid` | GridID |
| `FetchTileContent` | TileID |
| `DropTileContent` | ContentID |
| `Await` | Token, Request |

`Request` is closed: `GetTile{ID}`, `GetGrid{ID}`, `ReadContent{TileID}`,
`Search{Query, Scope, Limit}`, `ProbeShell{ContentID}`.

**URL, menu, feedback** (5)

| Effect | Payload |
|---|---|
| `OpenMenu` | PaneID |
| `CloseMenu` | — |
| `ScheduleURLUpdate` | — |
| `WriteURLNow` | — |
| `PlaceCursor` | Col, Row |

**Mutation and the second axis** (4)

| Effect | Payload |
|---|---|
| `DeleteEphemeral` | GridID, TileID |
| `Report` | Severity, Source, Message |
| `EnterLevel` | PaneID, TileID |
| `LeaveLevels` | Count |

`EnterLevel` and `LeaveLevels` are how a pane verb calls the level axis. In
phase A the executor runs the existing `descendLevel` / `ascendLevels`; in
phase C those become machine verbs that emit `InstallLevel` / `PopLevel`. The
vocabulary does not change between phases.

There is no `Redraw`. The executor draws once after a plan, always.

## 2. The machine

```go
package nav   // js-free, unit-tested

type Machine struct{ … }        // suspended continuations and barriers

func New() *Machine
func (m *Machine) Do(g Gesture, w World) Plan
func (m *Machine) Resume(tok Token, r Result, w World) Plan
func (m *Machine) Land(tok Token, w World) Plan   // a transition finished or was cancelled

type Plan struct{ Effects []Effect }
```

Verbs (`Gesture`): `Descend{PaneID, Door rpc.Tile, After *Token}`,
`Ascend{PaneID, N int, Animate bool}`, `Restore{PaneID, Raw string}`,
`RestoreFromHistory{Raw string}`, `Promote{OriginPaneID, DestPaneID, OldID,
Created}`, `EnterLevel{PaneID, TileID}`, `LeaveLevels{Count}`,
`ReEngage{PaneID, TileID}`.

### Async: continuation tokens, not generation counters

A token is minted by the machine, carried on the effect that starts the wait,
and handed back with the answer.

```go
type Token uint64

type cont struct {
    Guard   Guard    // must still hold when the answer lands
    Step    Step     // what the machine does next; a closed set
    Barrier BarrierID // optional join
    Data    …        // the descent-time facts the step needs
}

type Guard interface{ … }   // closed:
//   Always
//   PaneExists{PaneID}
//   DescendedIn{PaneID, TileID}     — today's pane.StillDescended
//   PaneUntouched{PaneID, Anchor}   — fallbackTreeFor's re-centre guard
//   LevelTopIs{TileID}              — the rename and layout-flush guard
```

`Resume` evaluates the guard against the fresh snapshot. Guard false: the
continuation is retired and the plan is empty. That is the whole moved-on
rule, spelled once, in one place, for every path.

Chosen over a per-pane generation counter for three reasons.

1. A counter is a second copy of "where is this pane". `pane.Stack` owns that
   (`ARCHITECTURE.md`, one fact one owner). A guard is a *projection* of the
   stack in the snapshot, computed at resume, stored nowhere.
2. The existing guards are not "nothing changed" — they are different
   preconditions per path: still descended in this tile; still sitting
   untouched at this anchor; still the top level. A counter cannot express
   them, so adopting one would change behavior at every site.
3. A token carries its data. `descendContent` today captures `fileCopy` by
   value in a closure because an ephemeral scratch tile is in no cached grid.
   On a continuation that is a field, visible to a test.

Tokens are also the barrier mechanism. A pane-tile descent has two arms — the
animation and the fetch — that join before the install (`wsPending` today).
`Barrier{ID, Arms: 2}` resumes the joined step when the last arm reports; a
newer barrier on the same pane supersedes the older one, which is dropped
without running. A failing arm resumes a different step (the origin-viewport
restore).

Tokens are the machine's only mutable state. They are session-scoped, are
never persisted, and every one is retired by exactly one of: a resume, a
land, or a guard that no longer holds.

## 3. The world snapshot

Gather then execute, the shape `dragdrop.DropInput` already established
(`client/dragdrop/dragdrop.go`): every impure read resolved up front, no App
field and no `js.Value` inside the package, so a field cleared mid-teardown
can never be read late.

Common to every verb:

```go
type World struct {
    Focus      string
    Panes      []PaneView   // ID, Stack (clone), Cx/Cy/Zoom, TextScroll*, TextMode, Rect, OnScreen
    Home       string
    CellPx     float64
    TransitionMs float64
    Animating  map[string]bool     // trans.Active per pane
    MenuOpenOn string              // menu.OpenOn
    Caps       caps.Caps           // LiveURL, LiveShell
    Surfaces   []pane.Holder       // urlSurfaces + shellSurfaces: live-view presence
    LevelDepth int
    LevelTop   *pane.Level
}
```

Per verb, additionally:

- **Descend**: the door row by value; `DeadLink bool` (`deadref.DeadTile`);
  `ChildGridCached bool`; `Health *pluginhealth.Notice` (resolved only for a
  link with no child grid); `ReadOnly bool` (`tileReadOnly`);
  `PaneScratch scratch.Grid` (Cached + ScratchGridID); `PaneGridID string`.
- **Ascend**: `DescendedTile *rpc.Tile` (`descendedTile`, which walks the
  cache for an off-grid ephemeral visit); `DoorTile *rpc.Tile` plus
  `DoorGridCached bool`; `RootFraming *rpc.Framing` for the anchor's plugin;
  `ShellAlive map[string]bool` and `ShellAliveKnown map[string]bool`;
  `PaneScratch`.
- **Restore**: `CachedGrids map[string]bool`; `GridLoadFailed
  map[string]bool`; the decoded `pane.URLState` is the machine's own
  (`pane.DecodeURL` is already pure).
- **Promote**: `Created rpc.Tile`; `OldTile *rpc.Tile` (`cachedTileByID`);
  `LiveURL string` (the view's current address).

Facts the machine **projects** rather than receives, because a snapshot field
would be a copy: `pane.FramingTarget`, `pane.FramingWriters`,
`scratch.Ephemeral` over `PaneScratch`, `pane.StillDescended`,
`pane.ContentPanes`, `pane.TakeOver`, `OtherPaneShows`, `zoomtrans.*`,
`shellconn.DecideAutoLive`, `textedit.DescentMode`, `urlwalk.Walk`.

Three-valued facts stay three-valued. `ChildGridCached`, `DoorGridCached` and
`scratch.Grid.Cached` distinguish "no" from "not known yet", exactly as
`client/scratch` requires. A snapshot never guesses.

## 4. Phases

Each phase is green on `make check` and `make check-e2e` before the next. The
e2e suite is the no-behavior-change oracle.

### Phase A — descend and ascend

Absorbed: `descend`, `descendGrid`, `descendContent`, `ascend`, `ascendOnce`,
`leaveFrame`, `landingView`, `landOnFrame`, `autoLiveOnDescent`.

`nav.go` keeps the per-verb world resolvers (`navWorldForDescend`,
`navWorldForAscend`) and gains `nav_exec.go`: one `runNav(Plan)` switch, one
executor func per effect, each a direct move of the body it replaces.
`EnterLevel` and `LeaveLevels` execute by calling the untouched
`descendLevel` / `ascendLevels`.

Tests, all table-driven in `client/nav`:

- descend: well; link tile (frame carries the target grid id; menu captured);
  synthetic + menu well (footprint re-centre); content text; content read-only
  (drop-then-fetch); content url; content shell; page tile; dead link (empty
  plan); link with no child grid and a health notice; link with no child grid
  and none; workspace kind (`EnterLevel`).
- ascend: depth 1 (no-op); instant, no door row cached; animated out of a
  grid; animated out of content; out of a root grid with no doorway; + menu
  portal with no doorway row; ephemeral leave (delete, no freeze); ephemeral
  leave with a split sibling showing it (no delete); multi-hop N>1 (only the
  last animates).
- races: pane closed before the descent lands; pane closed before the ascent
  lands; a second descent arriving mid-animation (the first lands first, and
  the second's segments read the landed place); moved on mid-probe
  (`AutoLiveProbeShell` resumes with `DescendedIn` false); a shell probe
  answering dead.

Done when: those functions contain no policy, only resolution and execution;
the effect vocabulary is frozen; `make check` and `make check-e2e` green.

### Phase B — restore

Absorbed: `applyURLState`, `restoreFromHistory`, `autoLiveOnRestore`,
`healStalePanePath`.

The URL walk becomes the token loop: the machine drives `urlwalk.Walk` one
step at a time, emitting `Await{GetGrid}` on a cache miss and resuming with
the answer. `fetchGridSync` disappears — nothing in the machine blocks.
`autoLiveOnRestore` becomes `Await{GetTile}` guarded by
`DescendedIn`; `healStalePanePath` becomes `Await{Search}` with the same
guard re-checked at resume.

Tests: boot with no anchor; boot with a viewport; boot into a content leaf
(mode from the cached row, and from no row); a path whose middle id is
missing (loose walk); a grid that fails to load mid-walk; popstate while a
transition is running; pane closed mid-walk; moved on mid-heal; heal skipped
for an ephemeral visit; heal skipped when the path already resolves.

Done when: `urlsync.go` holds the DOM half only — `location`, `history`, the
textarea cursor — and every walk and restore decision is in `client/nav`.
(Landed. `urlsync.go` keeps the framing persisters, which are writeback, not
navigation.)

### Phase C — levels and promote

Absorbed: `descendLevel`, `installWorkspace`, `ascendLevels`,
`bootWorkspace`, `finishPromote`, `fallbackTreeFor`'s re-centre.

`wsPending` becomes a barrier. `maybeInstallWorkspace` disappears.

Tests: animation finishes first; fetch finishes first; fetch fails after the
animation lands (origin viewport restored); a superseded pending; a pane link
that redirects to its target; a never-arranged tile (capture at install); an
undecodable blob (read-only, never persisted); boot restore with no outer
tree; ascent with an outer tree; ascent with none (fallback re-centre, and
the re-centre skipped because the user moved); promote of an ephemeral url;
promote where a split sibling still shows the visit (no delete); promote
where the origin pane moved on mid-create.

Done when: `workspace.go` is the level executor plus the persister, and no
`StillDescended` or hand-written moved-on check remains anywhere in
`client/wasm`, shown by grep.

## 5. W6 rider — the App struct

The machine absorbs `wsPending` (a barrier), `urlPrevPlace`, `urlPlaceSeen`
and `urlRestoring` (the push-against-replace baseline and the
restore-in-flight state are navigation facts), and every moved-on closure the
current code captures. `App` gains one field, `nav *nav.Machine`.

**`a.fetch`** — `gridFetch`, `contentFetch`, `tileFetch`, `gridLoadFailed`,
`tileLoadFailed`. One owner of "a read is outstanding or has failed", built in
one place at boot. Every claim's life stays `client/inflight`'s; the grouping
only makes a reach from an unrelated file read as `a.fetch.…`.

**`a.overlays`** — `textTextarea` and its two callbacks, `textareaReady`,
`lastTextareaTileID`, `renderedView`, `renderedReady`, `lastRenderedKey`,
`textToggleBtn`/`textToggleCb`, `renameEditing`, `urlModalOpen`, `wsExpand`.
The DOM singletons and the "is this one showing" flags that go with them.
Constructed lazily as now, but through one accessor per singleton.

**`a.persist`** — `sched`, `textSaves`, `out`, `wellWheelPending`,
`persistPosts`, `framingFlushes`. The debounce schedulers, the write queue,
the outbox and the two e2e counters that instrument them. The machine emits
`Flush*` effects; this group is what executes them.

**`a.views`** — `urlPreview`, `wrapCache`, `renderedPrev`, `paneLayouts`,
`menuCtxs`. Caches and memos, never facts: every one is a derived view that
may be dropped wholesale without losing anything the user made. Grouping them
makes that property visible at the call site.

The remainder — `cl`, `home`, `tree`, `c`, `ws`, `locals`, `menu`, `errs`,
`trans`, `shells`, `caps`, `plugins`, `traces`, the drag states — stays on
`App`. Each already has one owning file.

## 6. What does not move

- `transition.Set` stays in `client/transition`. The machine emits
  `StartTransition` and `CancelTransition`; the set keeps the clock, the
  displaced-lands-anyway rule, and the per-pane invariant.
- Effect execution stays impure and stays in `client/wasm`. Every RPC, every
  DOM write, every `js.Value` is on that side of the seam.
- `pane.Stack` stays the owner of a pane's place. The machine reads a clone
  and returns a new one; it holds no place of its own between calls.
- The existing verdict owners stay: `shellconn.DecideAutoLive`,
  `scratch.Ephemeral`, `deadref.DeadTile`, `pane.FramingTarget`,
  `pane.FramingWriters`, `pane.SurfaceOf`, `pane.TakeOver`,
  `textedit.DescentMode`, `urlwalk.Walk`, `zoomtrans.*`, `pane.URLPushesEntry`.
  The machine calls them; it does not re-derive them.
- "Ctrl-click descends in a split below" stays `dragdrop.DecideDrop`'s
  (`DropNavigateSplit`). The shim performs the split and names the resulting
  pane in the `Descend` gesture.
- The debounced persisters keep their windows and their arming from `draw()`.
- No behavior change in any phase. A change that alters what the user sees is
  a separate commit with its own decision.

## Deviations found in phase A

Implementation found seven places where this design did not match reality.
Behavior is unchanged in every one; the design is amended here rather than
diverged from silently.

1. **`Plan` gains `Next *Gesture`.** Two paths must compute from state the
   effects above them just changed, exactly as the sequential code did: a
   descent or ascent that first lands a running transition (`trans.Cancel`
   runs the displaced landing, which moves the pane, before the segments are
   read), and each hop of a multi-level ascent (each `ascendOnce` reads the
   place the previous one landed on). Both return a plan that ends there and
   hands the rest back as a continuation GESTURE; the shim gathers a fresh
   world and calls `Do` again. The loop is bounded (`navGestureSteps`) and
   each step consumes something, so it always terminates. The alternative —
   letting the machine guess the post-cancel place — would have been a second
   copy of where a pane is.

2. **Thirty-three effects, not thirty-one.** `ScaleContent{PaneID}` is the
   content render scale (issue #82), re-derived at every landing on a content
   frame; the animated ascent's landing installs no place, so `InstallPlace`
   could not carry it. `ReEngage{PaneID, TileID}` is the restore path's
   go-live arm: phase A must not touch `autoLiveOnRestore`, so the effect runs
   it unchanged and phase B replaces its body with `Await{GetTile}` under a
   `DescendedIn` guard.

3. **`Gesture.Descend` has no `After`.** No call site passes one — the
   URL-tile creation path stopped chaining on it — so the dead parameter went
   with the absorbed function rather than into the vocabulary.

4. **`ReEngage` is a gesture in phase A.** `autoLiveOnRestore` still fetches
   and heals on the shim side, but its verdict routes through the machine, or
   `shellconn.DecideAutoLive` would have had two callers in the client.

5. **The snapshot carries four more resolved facts.** `World` gains
   `ZoomDistFactor` and `TextSideInset` (the renderer constants the segment
   math and the content fit zoom read) and `PaneView.GridID` (the grid a
   pane's place names, which only the cache walk can answer). `LeaveWorld`
   folds `RootFraming` into `LandingView`, the already-resolved viewport, and
   adds `DoorGridID`/`DoorGridCached`, because the ascent fetches that grid
   when it is missing.

6. **The barrier is data only until phase C.** `BarrierID` and the
   continuation's `Barrier` field exist; the arrival counting lands with its
   first user, because `make check`'s deadcode gate refuses a function no
   shipped binary reaches.

7. **The ascent calibrates against the doorway row AFTER its framing write.**
   `leaveFrame` persists the pane's centre onto the doorway and then reads it
   back, so the frame swap matches where the user actually is. Both halves
   of one plan, so the machine projects the same write onto its own copy
   through `zoomtrans` — which owns the formula and is the only owner either
   side reads.

`Effect` and `Gesture` are one tagged struct each rather than a type per
verb, the shape `pane.TreeNode` already uses: a plan is then comparable field
by field in a table test.

## Deviations found in phase B

Six more, again with no behavior change.

1. **`Gesture.Restore` gains `Reset`, and `PaneID` may be empty.** The
   popstate restore's level exit swaps the whole pane tree, so which pane is
   focused — and everything the per-pane reset reads off it — can only come
   from a world gathered after it. `RestoreFromHistory` therefore ends at
   `LeaveLevels` and hands the rest back as `Restore{Reset: true}` through
   `Plan.Next`, the mechanism deviation 1 established. `Reset` carries both
   halves of "this came from the browser": the reload-equivalent teardown at
   the start, and the URL handed back at the end. An empty `PaneID` means
   the focused pane, which is the only pane a restore ever targets.

2. **`ReEngage` becomes fetch-first, and stays an effect.** Phase A's
   `ReEngage` gesture took the row; it now takes a tile id and mints
   `Await{GetTile}` itself, with the heal's `Await{Search}` between the
   answer and the go-live verdict. The `EffReEngage` effect stays in the
   vocabulary and re-enters the machine through that gesture, because
   `workspace.go` has two call sites of its own (phase C absorbs them) and
   the alternative was two spellings of the same arm.

3. **A boot pane-tile restore rides `EffInstallLevel` with `TileID` alone.**
   `?w=` names a level to install, which is that effect's verb; everything
   else about it — fetching the row, following a pane link, decoding the
   blob — is still `bootWorkspace`'s until phase C, exactly as `EffEnterLevel`
   still runs `descendLevel`. No verb was added: the vocabulary stays at 33.

4. **`World.Restore` carries the whole cached grid set.** Which grids a path
   reaches is what the walk itself decides, so a gatherer that projected a
   subset would be running the walk. Gather-then-execute admits no callback
   left open across the seam, so the snapshot holds the cache as
   `urlwalk.Walk` reads it, plus the grid-load latch and every plugin root's
   persisted framing. It is resolved for restores only — boot and popstate,
   a handful of times a session.

5. **The walk re-runs rather than resuming mid-loop.** `urlwalk.Walk` takes a
   lookup callback, and a callback cannot suspend. The walk is pure and
   deterministic, so a cache miss suspends the whole restore on
   `Await{GetGrid}` and the walk is re-run against the warmer snapshot. The
   continuation records every grid already asked for, which is
   `fetchGridSync`'s one-shot rule made explicit — a transport failure
   latches nothing, so without it a grid that will not load would be asked
   forever. Bounded by the number of ids in the address.

6. **A restore's continuations are the restore's, not a pane's.**
   `Machine.Forget(paneID)` retires by pane, and forgetting the pane is a
   step the restore itself performs, so a pane-keyed continuation would
   cancel the restore mid-reset and leave the URL suppressed for the rest of
   the session. They carry `GuardAlways` and check the pane inside the step,
   which ends the restore — and hands the address back — when it is gone.

`Result` gained `Tile` and `Wells`; a search with no hit arrives as `OK`
false, the same "no" as a search that could not run, because both leave the
pane where it is. `PaneView` gained `Scratch`, replacing the per-verb copies
in `DoorWorld` (dead) and `LeaveWorld`: the ascent's ephemeral delete and the
restore's heal ask one question of one field.

One branch is kept without a test because the walk cannot reach it: a content
leaf whose row is not cached. The walk only names a leaf it read, so the
uncached default is the same defensive fallback `applyURLState` carried.

## Open question for the owner

`autoLiveOnRestore` fires an unconditional `GetTile` for every restored
content pane — on boot, on every level install, and on every ascent landing
back onto a content frame. As a machine step that is one `Await` per pane per
restore. Should it serve from the cached row when one is present, or stay an
unconditional refetch? Serving from cache is fewer round trips on a wide
layout; refetching is what makes a link's target and a moved tile resolve.
This is a freshness change, so it is not made silently under W1.
