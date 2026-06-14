# Gridwell refactor plan

A staged plan to drive the codebase toward: testable code, complexity removed or
well-tested, a dismantled `App` god-object, honest names, and zero leftover cruft.

**Where things stand:** Phases 0–2, most of Phase 3, and part of Phase 6 are **done and
merged to `main`** (see "Done so far"). What's left is interaction code (gestures,
transitions, live tiles) plus the wire-format swap — work that needs a human to watch the
UI. The actionable forward plan is **"Remaining work — roadmap" (R1–R6) at the bottom**;
start there. The Phase 0–7 sections above it are kept as the original design rationale and
as detail the roadmap items point back to.

## Guiding principles

1. **Move complexity into pure functions; test those exhaustively.** The js/SQL
   shell stays thin. Prefer *deleting* complexity over testing it.
2. **Refactor order: extract pure logic → table-test it → rewire the shell to call
   it.** Never refactor the shell and the logic in the same commit.
3. **Behavior-preserving until a phase explicitly changes behavior.** Phases 1–4 and
   6 change no user-visible behavior. Phases 5 and 7 do; they carry extra verification.
4. **Commit incrementally** — one logical change per commit (project rule). Each
   commit leaves all four gates green.
5. **The store is the correctness core.** Its safety net is `property_test.go` +
   `drift_test.go`; extend them whenever store invariants are touched.
6. **Preserve the mutation model (guardrail — do not regress).** Tiles are mutated
   **in place**: an edit is an `UPDATE … WHERE id = ?` (+ in-place `version` bump for
   content edits). There is **no version history / Git-style revisioning**. The only
   row-copying is the COW *write-isolation* fork (`preWrite`→`forkGrid`) that fires at
   most once, on the first divergent edit of a tile in a *shared* (cloned) grid — it
   copies rows (new integer `id`, same `object_id`+`version`) so the edit can't leak
   into sibling clones; the pre-fork rows are the siblings' live data, not history. New
   `object_id`s are born only from `Create*` and the source-grid reconciler; `CloneTile`
   is the only thing that makes two rows share an `object_id`. Every refactor must keep
   this exactly; `property_test.go` guards it.
7. **Working mode for the remaining phases (human-in-the-loop, visual checks).** The
   remaining work is mostly interaction code (gestures, descent/ascent transitions, live
   tiles) that **cannot be verified by the four gates alone** — it needs a human to watch
   the UI. So each remaining unit is split into two halves and committed separately:
   **(a) the pure half** — pull every decision/geometry/state-transition that *can* be a
   pure function into a `client/*` package and **table-test it exhaustively** (this is
   still done autonomously; it's the bulk of the logic and the whole point — maximize the
   surface that's manipulable without a human). **(b) the thin wiring half** — the
   remaining `App`/`js` glue that calls the pure half and applies its result. Keep (b) as
   small and obvious as possible, land it in its own commit, and **pause for the user to
   visually verify** before moving on. Never bundle a pure extraction and a behavioral
   rewire in the same commit, and never do (b) for two units before the first is verified.

## The verification gate (run after every commit)

```sh
go build ./...
go test ./...
GOOS=js GOARCH=wasm go build -o /tmp/gridwell.wasm ./client/wasm
cd apps/desktop && npm run typecheck
```

**Deliverable, Phase 0:** add a `make check` target that runs all four so every
commit is verified identically. Electron harnesses (`npm run test:integration`,
`npm run test:bridge`) need a display/xvfb and are run only in phases that touch the
live-tile path (5) and at the very end.

---

## Phase 0 — Baseline & guardrails

- [x] Confirm the four gates are green on a clean tree; record any pre-existing
      failures so we don't chase them later. (All four green; no pre-existing failures.)
- [x] Add `make check` (the four gates) and `make check-electron` (the two xvfb
      harnesses). Wire `check` into the default expectation for "done with a commit."
- [x] Create a working branch off `main`. (`refactor/cleanup-and-testability`)

**Done when:** `make check` is green and is our per-commit ritual.

---

## Phase 1 — Delete confirmed dead code (safe, mechanical)

These are defined + unit-tested but have **zero production callers** (verified by
grep). Deleting them *and their tests* removes false coverage.

- [x] `internal/store/url.go` — `SetURLPreview` (rod-era; superseded by `SetURLState`).
      Audit `url_test.go`: delete tests *of* `SetURLPreview`; any that merely use it to
      set up preview state get rehomed onto `SetURLState`.
- [x] `client/dragdrop/dragdrop.go` — `EdgeBand`, `IsInEdgeZone` (edge-band ascent
      removed), `PaneAt`, `FootprintFits`. Trim `dragdrop_test.go` accordingly.
- [x] `client/panebox/panebox.go` — `StreamLocalCoords` (old WS-URL coord map) + its test.
- [x] `client/anim/anim.go` — `SplitDuration` (superseded by `SplitN`), `Distance`
      (no caller) + their tests.
- [x] `client/cache/cache.go` — `InvalidateBlob` + its test.
- [x] `client/pane/pane.go` — `SplitOnSide` (superseded by `SplitOnSideAt`) + its test.
- [x] `client/pane/pane.go` — `TruncatePathTo` + its test. **DECIDED: delete.** (It
      implied a "trim stale descent path after delete" behavior that was never wired;
      revisit only if path-staleness bugs surface.)

Commit grouping: one commit for store, one for client helpers (or per-package).

**Done when:** all symbols gone, gates green, coverage reflects only live behavior.

---

## Phase 2 — Store: consolidate refcount + blob logic (correctness-positive)

The highest-risk code; strongest testing. No behavior change.

- [x] **Fix the invariant violation.** `source_grid.go:deleteFSGridTile` hand-rolls
      per-kind refcount decrements (and ignores `preview_blob_id`), directly violating
      CLAUDE.md's "never hand-roll a per-kind inc/dec." Replace its body with
      `s.decTileRefs(ctx, tx, t.Kind, t.ChildGridID, t.BlobID, t.PreviewBlobID)`.
- [x] **Extract `swapTileBlob`.** One helper:
      `swapTileBlob(ctx, tx, tileID int64, col string, bytes []byte) (newBlobID int64, changed bool, err error)`
      that does `putBlob` → `UPDATE tiles SET <col> = ?` → inc-new/dec-old when changed.
      Replace the hand-rolled copies in: `SetShellPreview`, `SetURLState` (jpeg branch),
      `UpdateText` (blob branch), `refreshProcInfoBlob`. Callers keep their own
      version-bump and extra-column writes (alt/url/title); only the blob-swap kernel
      moves. (`SetURLPreview`'s copy is already deleted in Phase 1.)
- [x] **DRY the well-kind checks.** Replace explicit
      `Kind == well || file-well || process-well` with `isWellKind` in `deleteFSGridTile`,
      `dropTargetAt` (client), `childTileAtScreen` (client). (Client ones can ride along
      here or in Phase 3 — keep store and client commits separate.) — store side done
      (`deleteFSGridTile` now routes through `decTileRefs`, no kind check); **client
      `dropTargetAt`/`childTileAtScreen` deferred to Phase 3.**
- [x] **Tests.** Extend `property_test.go` so the generated tile population includes a
      source-grid (fs/proc) holding each tile kind, and `verifyRefcounts` runs after a
      reconcile-driven delete — this would have caught the `deleteFSGridTile` drift. Add
      a focused unit test for `swapTileBlob` (dedup hit, change, no-op identical bytes).

**Done when:** property + drift tests pass with the new coverage; the blob-swap dance
exists in exactly one place.

---

## Phase 3 — Carve up the wasm mega-files; extract testable logic

`input.go` (2050), `render.go` (1975), `right_button.go` (1579) conflate many
concerns. Each sub-phase is its own commit(s). No behavior change.

### 3a — Mutation dispatch layer (out of input.go)
- [x] Move `postCrossGridMutate`, `postTileMutate`, `doTileMutate`, `postUpdateText`,
      `postTwoGridMutate`, `postPersist`, `isVersionConflict`, `logRPCError`,
      `tileCall`/`voidCall` into `client/wasm/mutate.go`.
- [x] Extract the **error classification + reaction decision** into a pure helper in a
      new `client/clientsync` package: `Classify(err, conflict) → Reaction{Refetch, Log}`.
      Table-tested. `App.reactToErr` detects the transport conflict and applies it.

### 3b — Markdown render engine (out of render.go)
- [x] Moved `drawMarkdown*`, `wrapInline`, `drawInlineLines`, `markdownStyle`,
      `defaultMarkdownStyle`, `setFont`, `embedLogicalSize` into `markdown_render.go`.

### 3c — Glyph + palette drawing (out of render.go)
- [x] Moved glyph primitives + `draw*Glyph` + swatch + trashcan into `glyphs.go`; the
      palette layout adapters + popover drawing into `palette_draw.go`. render.go 1974→1116.

### 3d — Right-button gesture state machine → pure package
- [x] **Pure geometry hoisted + tested.** `splitChildRects` (was a verbatim dup of
      `pane.splitRect`) → exported `pane.SplitRect`; `splitRatioFromPos` →
      `pane.SplitRatioFromPos`; `clampResizeRatio` → `pane.ClampRatioToMinPx`. All
      table-tested in `client/pane`.
- [x] **Drawing carved out.** Gesture previews → `gesture_draw.go`. right_button.go
      1578→892.
- [ ] **MOVED to R2 (human-in-the-loop).** The full `Outcome` sum-type classifier rewire
      was deferred while there was no way to watch the gesture UI; it now proceeds as
      roadmap item **R2** — pure `gesture` package + table tests (autonomous), then a thin
      rewire of `onRightDown/finishRightDrag` that the user visually verifies.

### 3e — Unify drop resolution (kills left/right drag duplication)
- [ ] **MOVED to R3 (human-in-the-loop).** Proceeds as roadmap item **R3** — pure
      `DropOutcome` + table tests (autonomous), then a thin rewire of the move/clone commit
      paths that the user visually verifies. The pure sub-win (`rpc.IsWellKind`, used by
      `dropTargetAt`/`childTileAtScreen`) already landed with the Phase-2 carryover.

**Done when:** the three files are materially smaller and split by concern (✓: render
1974→1116, right_button 1578→892, input 2049→1895); pure logic that *can* be safely
extracted is in tested packages (✓: clientsync, pane geometry); the two behavior-rewiring
items are deferred with rationale above.

---

## Phase 4 — Descent/ascent transition: make it testable (and simpler)

The most complex untested logic in the client (four near-parallel `start*` methods +
segment machinery + `onComplete` focus-restore), welded to `App` + `js`.

- [ ] **MOVED to R4 (human-in-the-loop).** Proceeds as roadmap item **R4** — pure
      `client/transition` `Plan` builder + table tests (autonomous; the App-state resolution,
      duration apportioning, saved-vs-calibrated landing, and missing-level fallbacks become
      data), then collapse the four `start*` methods into one builder + visual check. The
      transition *math* is already pure+tested in `zoomtrans`/`anim`; R4 makes the
      *construction* pure too, leaving per-frame interpolation as the only js remnant.

**Done when:** transition construction is pure and tested; the four methods are one — via R4.

---

## Phase 5 — Unify URL + shell into a "live media tile" abstraction (behavior-touching)

URL and shell are near-duplicate "frozen JPEG → go live → capture-freeze → park during
gesture → per-frame position sync" tiles implemented twice.

- [ ] Define the shared lifecycle (client side): an interface/struct capturing
      `place/activate`, `freeze(capture→SetPreview RPC)`, `parkDuringGesture`,
      `syncToContentBox(rect)`, with kind-specific hooks: URL → Electron bridge
      (`webview_bridge.go`); shell → xterm + `/rpc/ShellStream` WS.
- [ ] Collapse `syncURLViews`/`syncShellOverlayPosition`, the two open/close/closeAll
      pairs, and the `liveOverlaysHidden` consumers into the shared manager. `urlPreview`
      cache already serves both — keep it.
- [ ] Store side: `SetShellPreview` and `SetURLState` already share `swapTileBlob`
      (Phase 2). Decide whether they merge into one "freeze preview" RPC + an optional
      url/title sidecar, or stay separate (they carry different extra fields). Default:
      keep two RPCs, share the store kernel — don't force a merge that adds branching.
- [ ] **Verification (this phase changes live behavior):** `make check`, plus the
      Electron harnesses, plus the manual L4 checklist: descend a URL tile → go live →
      navigate → ascend (frozen frame persists) → mirror in a 2nd pane; descend a shell →
      refresh (PTY spawns) → ascend (frozen frame) → re-descend → refresh reattaches.

**Done when:** one live-media code path serves both kinds; manual L4 passes.

---

## Phase 6 — Dismantle the `App` god-object + fix names/cruft

By now the sub-systems exist; group `App`'s ~50 flat fields into cohesive sub-structs it
holds, and make names tell the truth.

- [~] **Field grouping.** Collapse `App`'s fields into owned structs. **STARTED:** the
      seven debounce/raf fields are now `App.sched scheduler` (one group per commit, as the
      plan prescribes). **REMAINING (mechanical, lower priority):** `dom`, `net`, `view`,
      `drag`, `live`, `transition`, etc. Each remaining group pins at least one extremely
      hot field (`cctx`, `cl`, `tree`, `c` — 200+ references each), so it's a large but
      purely-organizational, compiler-verified sweep with no behavior/correctness gain.
      Best finished as a focused dedicated pass; the target table below is the map.
- [~] **Names.** Dropped the vestigial `_, _ int64` args on `openURLStream` (+ the dead
      `paneStreamSize`/`StreamViewportSize` helpers). **REMAINING:** rename
      `urlStreams`/`openURLStream`/`closeURLStream` off the rod/WS-era "stream" vocabulary —
      deferred to land together with the Phase-5 live-media manager (the natural home for
      the new names) rather than churn the names twice.
- [x] **migrations.go.** Removed the speculative `migration`/`applyMigrations` machinery;
      kept the `schema_version` stamp + the refuse-if-stored-newer guard.
- [x] **Docs.** Updated CLAUDE.md's "Architecture — where things live" to the new file
      layout (mutate.go, glyphs.go, palette_draw.go, markdown_render.go, gesture_draw.go,
      the pure client packages incl. clientsync) + the `make check` gate + swapTileBlob.

**Done when:** `App` reads as a small coordinator (partial: `sched` grouped, rest mapped);
no name lies about what it does (partial: vestigial args gone, "stream" rename pending with
Phase 5); migrations scaffold gone; docs synced.

---

## Phase 7 — Replace proto/Connect with JSON-over-HTTP + SSE (Option B — APPROVED)

**DECIDED: Option B.** Execute as the final phase. Because it changes the wire format
(largest blast radius), write a short sub-plan and re-run the full gate + Electron
harnesses + manual L4 before/after. DB is unaffected (testing-mode: wipe is fine).

Original framing retained below for reference.

### (reference) Paradigm decision: the proto/Connect layer

The heaviest abstraction relative to benefit: every wire field lives in **five** places
(DDL, `data.proto`, `rpc.*` struct, `ToProto`, `FromProto`), aligned only by a lint
test, for a single-tenant same-language same-repo loopback RPC. ~3,900 generated lines +
`conv.go` (334) + the `buf` toolchain.

**Do NOT start without an explicit go/no-go.** Options, in order of blast radius:

- **Option A (recommended default, low risk):** keep proto as the wire format and the
  streaming `Subscribe`, but eliminate the hand-written mirror: either generate `conv.go`
  or collapse `rpc.*` so a field is defined in ≤3 places. Drift test stays.
- **Option B (high value, high blast radius):** replace proto/Connect with JSON-over-HTTP
  + SSE using the `rpc` structs directly. Deletes `api/gen`, `conv.go`, `buf.*`, the
  proto, and the `connectrpc`/`protobuf` deps. `Subscribe` becomes a small hand-rolled
  SSE handler over the `rpc.Event` model that already exists internally. Changes the wire
  protocol → full re-verification, and the store DB is unaffected (testing-mode: wipe is
  acceptable).

Recommendation: **Option B** matches the "minimal maintenance, few new features" goal
best, but it's the user's call. Sub-plan to be written when the option is chosen.

---

## App field-grouping target (reference for Phase 6)

Current: ~50 flat fields on `App`. Target groups (names illustrative):

| Group | Holds |
|---|---|
| `dom` | doc, win, canvas, cctx, fileTextarea(+cbs), fileToggleBtn(+cb) |
| `net` | cl, gridLoadFailed, gridInflight |
| `view` | rootGridID, rootView{Cx,Cy,Zoom}, tree, c (cache), selectedTileID, menu* |
| `drag` | dragging, ghost, hiddenTileID/PaneID, leftResize, rightDrag, previewPane* |
| `live` | live-media manager (URL+shell), shellAlive/Probing, urlPan{X,Y}, urlPanDragging |
| `transition` | transition + paneStateStack |
| `sched` | rafScheduled, urlUpdate*, fileSave*, rootViewSave*, animation |
| misc | embedHits, urlModalOpen, lastTextareaTileID |

---

## Done so far (merged to `main`)

Phases 0–2 complete; Phase 3 complete except the two interaction-rewire items (3d-classifier,
3e-drop); Phase 6 partially complete. Highlights:

- **Gates** — `make check` (4 gates) + `make check-electron`; every commit green.
- **Dead code** — all 11 grep-confirmed dead symbols + tests deleted.
- **Store correctness** — `swapTileBlob` kernel (blob-swap dance unified across 4 callers);
  **fixed the `deleteFSGridTile` hand-rolled-refcount invariant violation**; new tests incl. a
  reconcile-delete refcount net *verified to fail* if the release is skipped.
- **wasm decomposition** — `mutate.go` + pure tested `clientsync`; render split into
  `glyphs`/`palette_draw`/`markdown_render` (1974→1116); gesture geometry hoisted to tested
  `pane` helpers + `gesture_draw.go` (right_button 1578→892); `rpc.IsWellKind`.
- **Cleanup** — migration scaffold removed; vestigial `openURLStream` args + dead viewport
  helpers gone; CLAUDE.md synced; App `sched` sub-struct (1st field group).

---

## Remaining work — roadmap (next phase: human-in-the-loop)

Ordered for execution. Per **principle 7**, every item below is split into a **pure half**
(extract → table-test, done autonomously, the priority) and a **wiring half** (thin App/js
glue, its own commit, **paused for the user's visual check**). The visual-check column lists
exactly what to click. Nothing here changes the store/wire model except R5/R6.

### R1 — Finish the `App` field grouping  *(mostly autonomous; no visual check)*
The only remaining item with no UI risk — compiler-verified, behavior-preserving.
- [ ] Group the rest of `App`'s flat fields into the sub-structs in the **target table
      above** (`dom`, `net`, `view`, `drag`, `live`, `transition`, misc), one group per
      commit, exactly as `sched` was done. Each: declare the struct, move the fields, sed
      `a.field`→`a.group.field`, `make check`. No visual check needed (a green build is
      proof); still, land one group per commit so any surprise is bisectable.
- Watch out: `cctx`, `cl`, `tree`, `c` are referenced 200+ times each — biggest seds.

### R2 — Right-button gesture classifier → pure `client/gesture`  *(3d)*
- [ ] **Pure (autonomous):** define `gesture` package. Inputs = (region from
      `pane.ClassifyRegion`, tile geometry, button, down/cur positions) → typed `Kind`; on
      release → typed `Outcome` sum type (`Split{side,ratio}`, `Swap`, `Resize{ratio,
      collapse}`, `TileResize{x,y,w,h}`, `Clone`, `Delete`, `Ascend`, `URLRefresh`,
      `Cancel`, `None`). Compose the *already-tested* pieces (`pane.ClassifyRegion/
      SplitClampedPosition/RatioFromCursor/ClampRatioToMinPx`, `dragdrop.Resize*`). Table-test
      classifier + every outcome's math (degenerate/collapse/crossover).
- [ ] **Wiring (visual check):** rewire `onRightDown`/`onRightMove`/`finishRightDrag` to call
      the pure classifier/outcome and only *apply* the result. `gesture_draw.go` stays.
- **Visual check:** over a tile — clone (center right-drag), resize (ring right-drag),
  delete (drag onto blackhole). Over a pane — split (each of 4 edges), swap, resize to
  collapse, ascend (corner circle + drag-out cancel), URL-refresh (down-drag in a URL descent).

### R3 — Unify drop resolution → pure `DropOutcome`  *(3e)*
- [ ] **Pure (autonomous):** extract the "cursor → drop outcome" decision (doc-drop vs
      blackhole-sink vs same-cell-noop vs overlap-reject vs target-cell vs into-well) into the
      `gesture` (or a `drop`) package as a typed `DropOutcome`. Table-test it.
- [ ] **Wiring (visual check):** `onMouseUp` (move) and `commitRightClone` (clone) both call
      it, differing only in the final RPC; one shared ghost-target updater for the live-preview
      branches (`onMouseMove` vs `advanceCloneDrag`).
- **Visual check:** left-drag move within a grid and into a well preview; right-drag clone
  same; drag onto a blackhole (both buttons); drop onto an occupied cell (reject); drag a
  file-well tile into a regular grid (link/dashed); no-op same-cell release.

### R4 — Descent/ascent transition planner → pure `client/transition`  *(Phase 4)*
- [ ] **Pure (autonomous):** given pane endpoints + well/file geometry + saved-state-stack
      entry + pane rect → a `Plan{segments []Segment, restore *FocusRestore}` as data
      (segments already plain floats/paths — no js). The duration apportioning, the
      saved-vs-calibrated landing, and the missing-level fallbacks live here. Table-test:
      descent continuity (preview cell == post-swap live cell), ascent lands on saved state,
      skipped-missing-levels → instant, embed-descent focus-restore.
- [ ] **Wiring (visual check):** collapse the four `start{Descent,Ascent,FileDescent,
      FileAscent}` into one builder that builds the `Plan` then installs segments; per-frame
      interpolation stays the only js part.
- **Visual check:** descend+ascend a well; a text tile (incl. via an embed click → ascent
  lands back in the doc); a URL tile; a shell tile; ascend across a deleted-mid-path level.

### R5 — Unify URL + shell into a "live media tile"  *(Phase 5; behavior-touching)*
- [ ] **Pure (autonomous) where possible:** extract the shared lifecycle state machine
      (place/activate → freeze(capture→SetPreview) → park-during-gesture → sync-to-content-box)
      as data/predicates and table-test the parts that don't need js (which overlay to park,
      when to freeze, bounds math via `panebox`). Kind-specific hooks stay: URL→Electron
      bridge, shell→xterm + `/rpc/ShellStream`.
- [ ] **Wiring (visual check):** collapse `syncURLViews`/`syncShellOverlayPosition`, the two
      open/close/closeAll pairs, and `liveOverlaysHidden` consumers into the shared manager.
      Store side: keep two RPCs, share the `swapTileBlob` kernel (already done) — don't force
      an RPC merge. Fold the **"stream" rename** (`urlStreams`→live-media name,
      `openURLStream/closeURLStream`→`placeLive…/freezeLive…`) in here.
- **Verification:** `make check` + `make check-electron` + manual L4 — URL: descend→go
  live→navigate→ascend (frozen frame persists)→mirror in a 2nd pane. Shell: descend→refresh
  (PTY spawns)→ascend (frozen frame)→re-descend→refresh reattaches.

### R6 — Replace proto/Connect with JSON-over-HTTP + SSE  *(Phase 7; Option B, APPROVED)*
Largest blast radius; **write a short sub-plan first.** DB is unaffected (testing-mode wipe ok).
- [ ] Delete `api/gen`, `conv.go`, `buf.*`, the `.proto`, and the `connectrpc`/`protobuf`
      deps. Serve the `rpc.*` structs directly as JSON over HTTP; `Subscribe` becomes a small
      hand-rolled SSE handler over the existing `rpc.Event` model. Mostly compiler- and
      `internal/server`-test-verified.
- **Visual check (smoke):** the client still boots, loads grids, and mutations round-trip
  (the only part the gates can't prove is the real client↔server wire).

**Suggested order:** R1 (autonomous warm-up) → R2 → R3 → R4 → R5 → R6. Do each item's pure
half fully (and get its tests green) before its wiring half; pause after each wiring half for
the visual check before starting the next item.
