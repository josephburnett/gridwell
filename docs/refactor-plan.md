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
- [ ] **DEFERRED (risk-based): full `Outcome` sum-type classifier rewrite.** Rewiring
      `onRightDown/finishRightDrag` through a new pure classifier is a behavior-preserving
      rewrite of the most intricate, **untested** interaction code, and this environment
      has **no way to exercise the gesture UI** (the Electron harnesses don't cover it).
      The math it would compose (`ClassifyRegion`, `RatioFromCursor`, `SplitClampedPosition`,
      `ResizeFromCursor`, plus the three helpers above) is *already* pure and tested, so the
      classifier would mostly re-encode an if-ladder while adding real regression risk
      against the zero-defects goal. Revisit once a gesture-level integration harness exists.

### 3e — Unify drop resolution (kills left/right drag duplication)
- [ ] **DEFERRED (risk-based):** same rationale as the 3d classifier — unifying the
      move/clone drop-outcome decision means rewiring two untestable commit paths with no
      UI harness to verify against. The pure, safe sub-win (`rpc.IsWellKind`, used by
      `dropTargetAt`/`childTileAtScreen`) landed with the Phase-2 carryover.

**Done when:** the three files are materially smaller and split by concern (✓: render
1974→1116, right_button 1578→892, input 2049→1895); pure logic that *can* be safely
extracted is in tested packages (✓: clientsync, pane geometry); the two behavior-rewiring
items are deferred with rationale above.

---

## Phase 4 — Descent/ascent transition: make it testable (and simpler)

The most complex untested logic in the client (four near-parallel `start*` methods +
segment machinery + `onComplete` focus-restore), welded to `App` + `js`.

- [ ] **DEFERRED (risk-based).** Same rationale as 3d/3e. The transition math is *already*
      pure and tested in `zoomtrans` (`Ascent`/`Descent`), `anim.SplitN`, and
      `zoomtrans.PanDist`/`ZoomDist` (the wasm `panDist`/`zoomDist` are one-line adapters).
      What remains in the four `start*` methods is App-state resolution (well lookup,
      saved-state stack, missing-level fallbacks) + mechanical `transSegment` assembly +
      the js-coupled per-frame interpolation runner. Extracting a pure `Plan` would mostly
      copy already-tested endpoints into a near-trivial struct, while collapsing the four
      methods is a behavior-preserving rewrite of untested descent/ascent interaction code
      with **no UI harness in this environment** to verify continuity/landing/fallbacks.
      Net risk to the zero-defects goal exceeds the marginal testability gain. Revisit with
      a transition-level integration harness.

**Done when (deferred):** transition construction is pure and tested; the four methods are
one. Not attempted blind — see rationale.

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

## Status

Branch: `refactor/cleanup-and-testability` (off `main`). Every commit leaves `make check`
green. Summary of where each phase landed:

- **Phase 0 — done.** `make check` / `make check-electron` gates; working branch.
- **Phase 1 — done.** All grep-confirmed dead code + its tests deleted (store + 6 client pkgs).
- **Phase 2 — done.** `swapTileBlob` kernel (blob-swap dance unified, 4 callers); fixed the
  `deleteFSGridTile` hand-rolled-refcount invariant violation; new tests (`TestSwapTileBlob`,
  reconcile-delete refcount net, verified to fail if the release is skipped).
- **Phase 3 — mostly done.** 3a `mutate.go` + pure tested `clientsync`; 3b/3c render split
  (`glyphs`, `palette_draw`, `markdown_render`); 3d-safe pure gesture geometry → `pane`
  (tested) + `gesture_draw.go`; `rpc.IsWellKind` DRY. **Deferred:** 3d full Outcome-classifier
  rewrite and 3e drop-unification — behavior-preserving rewrites of untested interaction code
  with **no UI harness in this environment** to verify; their math is already pure+tested.
  File sizes: render 1974→1116, right_button 1578→892, input 2049→~1890.
- **Phase 4 — deferred (risk-based).** Transition math already pure+tested in `zoomtrans`/
  `anim`; the rest is App-welded + js per-frame, unverifiable here. See Phase 4 rationale.
- **Phase 5 — not started.** Behavior-changing (URL+shell live-media unify); needs the
  Electron harnesses + manual L4 that this environment can't run. The store kernel it depends
  on (`swapTileBlob`) is already shared.
- **Phase 6 — partially done.** migrations scaffold removed; vestigial `openURLStream` args +
  dead viewport helpers gone; CLAUDE.md synced; App `sched` group extracted (1st of ~8).
  **Remaining:** the rest of the App field grouping (large mechanical sweep, hot fields) and
  the `urlStreams`/"stream" rename (to land with Phase 5).
- **Phase 7 — not started.** proto/Connect → JSON+SSE (Option B, approved). Largest blast
  radius; needs its own sub-plan + full re-verification. Not feasible to do well at this
  session's tail.

**Next-session priorities:** (1) finish the App field grouping using the target table above;
(2) Phase 7 with a written sub-plan; (3) the deferred interaction-code rewrites (3d/3e/4 + 5)
once a gesture/transition/live-tile integration harness exists to verify them.
