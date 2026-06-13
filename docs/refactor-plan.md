# Gridwell refactor plan

A staged plan to drive the codebase toward: testable code, complexity removed or
well-tested, a dismantled `App` god-object, honest names, and zero leftover cruft.
Worked start-to-finish; each phase is self-contained and resumable after a context
compression. **Check items off as they land.**

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

- [ ] `internal/store/url.go` — `SetURLPreview` (rod-era; superseded by `SetURLState`).
      Audit `url_test.go`: delete tests *of* `SetURLPreview`; any that merely use it to
      set up preview state get rehomed onto `SetURLState`.
- [ ] `client/dragdrop/dragdrop.go` — `EdgeBand`, `IsInEdgeZone` (edge-band ascent
      removed), `PaneAt`, `FootprintFits`. Trim `dragdrop_test.go` accordingly.
- [ ] `client/panebox/panebox.go` — `StreamLocalCoords` (old WS-URL coord map) + its test.
- [ ] `client/anim/anim.go` — `SplitDuration` (superseded by `SplitN`), `Distance`
      (no caller) + their tests.
- [ ] `client/cache/cache.go` — `InvalidateBlob` + its test.
- [ ] `client/pane/pane.go` — `SplitOnSide` (superseded by `SplitOnSideAt`) + its test.
- [ ] `client/pane/pane.go` — `TruncatePathTo` + its test. **DECIDED: delete.** (It
      implied a "trim stale descent path after delete" behavior that was never wired;
      revisit only if path-staleness bugs surface.)

Commit grouping: one commit for store, one for client helpers (or per-package).

**Done when:** all symbols gone, gates green, coverage reflects only live behavior.

---

## Phase 2 — Store: consolidate refcount + blob logic (correctness-positive)

The highest-risk code; strongest testing. No behavior change.

- [ ] **Fix the invariant violation.** `source_grid.go:deleteFSGridTile` hand-rolls
      per-kind refcount decrements (and ignores `preview_blob_id`), directly violating
      CLAUDE.md's "never hand-roll a per-kind inc/dec." Replace its body with
      `s.decTileRefs(ctx, tx, t.Kind, t.ChildGridID, t.BlobID, t.PreviewBlobID)`.
- [ ] **Extract `swapTileBlob`.** One helper:
      `swapTileBlob(ctx, tx, tileID int64, col string, bytes []byte) (newBlobID int64, changed bool, err error)`
      that does `putBlob` → `UPDATE tiles SET <col> = ?` → inc-new/dec-old when changed.
      Replace the hand-rolled copies in: `SetShellPreview`, `SetURLState` (jpeg branch),
      `UpdateText` (blob branch), `refreshProcInfoBlob`. Callers keep their own
      version-bump and extra-column writes (alt/url/title); only the blob-swap kernel
      moves. (`SetURLPreview`'s copy is already deleted in Phase 1.)
- [ ] **DRY the well-kind checks.** Replace explicit
      `Kind == well || file-well || process-well` with `isWellKind` in `deleteFSGridTile`,
      `dropTargetAt` (client), `childTileAtScreen` (client). (Client ones can ride along
      here or in Phase 3 — keep store and client commits separate.)
- [ ] **Tests.** Extend `property_test.go` so the generated tile population includes a
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
- [ ] Move `postCrossGridMutate`, `postTileMutate`, `doTileMutate`, `postUpdateText`,
      `postTwoGridMutate`, `postPersist`, `isVersionConflict`, `logRPCError`,
      `tileCall`/`voidCall` into `client/wasm/mutate.go`.
- [ ] Extract the **error classification + reaction decision** into a pure helper in a
      new `client/clientsync` package: given an RPC error → `{conflict bool, log bool}`
      (and the "refetch which grid / snap back" intent as data). Table-test it. The
      wasm wrapper becomes a thin goroutine + fetch/snapback applier.

### 3b — Markdown render engine (out of render.go)
- [ ] Move `drawMarkdown*`, `wrapInline`, `drawInlineLines`, `markdownStyle`,
      `defaultMarkdownStyle`, `setFont`, `embedLogicalSize` into
      `client/wasm/markdown_render.go`. Mechanical (same package). Layout decisions are
      already pure in `client/markdown`; this is file hygiene.

### 3c — Glyph + palette drawing (out of render.go)
- [ ] Move `draw*Glyph`, `beginGlyph`/`endGlyph`/`glyphBox`/`glyphLineWidth`,
      `drawPalette`, `drawPaletteTile`, `drawBlackHoleSwatch`, `drawTrashcanIcon` into
      `client/wasm/glyphs.go`. Mechanical.

### 3d — Right-button gesture state machine → pure `client/gesture` package
This is the high-value testability win.
- [ ] Define a pure `gesture` package: inputs = (region classification, tile geometry,
      cursor down/cur positions, button) → a typed `Kind`; on release → a typed
      `Outcome` sum type: `Split{side,ratio}`, `Swap{from,to}`, `Resize{ratio,collapse}`,
      `TileResize{x,y,w,h}`, `Clone`, `Delete`, `Ascend`, `URLRefresh`, `Cancel`, `None`.
      Pull in the existing pure pieces (`pane.ClassifyRegion`, `dragdrop.Resize*`,
      `SplitClampedPosition`, `RatioFromCursor`) so the package *composes* them into the
      decision rather than the wasm doing it inline.
- [ ] Table-test the classifier and every outcome's math exhaustively (incl. the
      degenerate/collapse/crossover cases that are currently only exercised by hand).
- [ ] Rewire `right_button.go`: `onRightDown/Move/finishRightDrag` call the pure
      classifier/outcome; the wasm only *applies* the Outcome (RPC or tree op) and draws
      previews. Drawing stays in wasm (`drawRightDragPreview` & friends, can move to
      `client/wasm/gesture_draw.go`).

### 3e — Unify drop resolution (kills left/right drag duplication)
- [ ] Extract the shared "cursor → drop outcome" decision into the `gesture` (or a
      `drop`) package: resolves doc-drop vs blackhole-sink vs same-cell-noop vs
      overlap-reject vs target-cell. Returns a typed `DropOutcome`.
- [ ] `onMouseUp` (move) and `commitRightClone` (clone) both call it; they differ only
      in the final RPC (`MoveTile` vs `CloneTile`). Same for the live-preview branches in
      `onMouseMove` vs `advanceCloneDrag` — one shared ghost-target updater.
- [ ] Table-test the drop resolution.

**Done when:** the three files are materially smaller and split by concern; the gesture
+ drop logic is in pure packages with table tests; gates green.

---

## Phase 4 — Descent/ascent transition: make it testable (and simpler)

The most complex untested logic in the client (four near-parallel `start*` methods +
segment machinery + `onComplete` focus-restore), welded to `App` + `js`.

- [ ] Extract a pure `client/transition` package (or extend `zoomtrans`): given current
      pane endpoints + well/file geometry + saved-state stack entry + pane rect → a
      `Plan{segments []Segment, restore *FocusRestore}` as **data**. Segments carry
      path-at-start + from/to (Cx,Cy,Zoom) + duration; the path-swap and the
      saved-vs-calibrated landing are computed here, not in wasm.
- [ ] Collapse the four `startDescent`/`startAscent`/`startFileDescent`/`startFileAscent`
      into one builder parameterized by descent-vs-ascent and well-vs-file. The wasm side
      becomes: build `Plan` (pure) → install segments → per-frame interpolate (the only
      js part left).
- [ ] Table-test the planner: descent continuity (preview cell == post-swap live cell),
      ascent landing on saved state, the "skipped missing levels → instant" fallbacks,
      and the embed-descent focus-restore patching.

**Done when:** transition *construction* is pure and tested; per-frame interpolation is
the only untestable remnant; the four methods are one.

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

- [ ] **Field grouping.** Collapse `App`'s fields into a handful of owned structs, e.g.:
      `dom` (doc/win/canvas/cctx, textarea, toggle), `net` (cl + grid load/inflight +
      failed), `view` (tree, root grid id + root view, selection, menu), `drag`
      (dragState + ghost + hidden + left/right resize), `live` (the Phase-5 manager +
      shellAlive), `transition`, `schedulers` (the four debounce flags + callbacks),
      `urlPan`/`embedHits`/`paneStateStack`. `App` becomes a coordinator of ~8–10
      sub-objects, not a bag of 50 fields. Do this incrementally (one group per commit);
      each group move is mechanical once its sub-system is already isolated.
- [ ] **Names.** Rename the rod/WS-era misnomers now that the abstraction is settled:
      `urlStreams`→ the live-media field name, `openURLStream/closeURLStream`→
      `placeLive…/freezeLive…`, drop the vestigial `_, _ int64` args on `openURLStream`.
      Update every comment that references deleted architecture (`rod`, `/rpc/URLStream`,
      "stream" where it's now a native view).
- [ ] **migrations.go.** Remove the speculative `migration`/`applyMigrations` machinery
      (currently `migrations = []`); keep only the `schema_version` stamp. (Re-add a real
      migration framework the day we exit "testing mode" — documented in CLAUDE.md.)
- [ ] **Docs.** Update CLAUDE.md's "Architecture — where things live" to the new file
      layout (mutate.go, gesture/, transition/, glyphs.go, the live-media manager).

**Done when:** `App` reads as a small coordinator; no name lies about what it does; no
dead-architecture comments remain.

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

## Status

- Current phase: **Phase 1** (Phase 0 complete).
- Branch: `refactor/cleanup-and-testability`.
