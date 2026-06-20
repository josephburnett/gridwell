# Plan: pure-extract the drag-drop commit decision (side quest)

**Why:** the trashcan-delete bug (commit `92f9b21`) lived in `client/wasm` glue that
`make check` compiles but never executes (`//go:build js && wasm` → excluded from host
`go test`; the electron harness loads dummy `data:` pages, not the renderer). Root cause
class: the **mousemove preview read live `a.dragging`, the mouseup commit read it after
teardown set it nil** — preview and commit disagreed. Goal of this work: move the drop
DECISION into a pure, host-tested function and route BOTH preview and commit through it so
they cannot diverge, and so the ordering/precedence logic gets real `go test` coverage.

**Home for pure code:** `client/dragdrop` (already host-buildable + tested; already holds
`MoveForbidden`, `FloorCellAt`, `Pane.CellAt`, etc.). Add to `dragdrop.go` /
`dragdrop_test.go`.

---

## MASTER EXECUTION ORDER (run top-to-bottom; COMMIT EACH numbered step; `make check` green every commit)

The user approved executing ALL tiers. Order is chosen warmup→centerpiece→rest, cheapest/safest
first. Detailed specs for each are in the sections below. Run `make check-electron` (expect
`HARNESS PASS` ×2) after any step that edits the wasm gesture handlers (steps 4a–4c).

1. **`client/textcursor`** — move the already-pure `offsetFromRowCol` (urlsync.go:333) into a new
   host pkg, add `RowColFromOffset` (lift the counting core out of `textareaCursorRowCol`
   urlsync.go:116 / `placeCursorAt` :320), repoint wasm callers, table-test (EOF, empty lines,
   single line, CRLF, negative/oob). [Tier1.1, S]
2. **Test top-ups** — add malformed-input tables to `client/url` (bad ids/floats, truncated paths)
   and edge cases to `client/shellconn`. Pure test additions, no prod change. [S]
3. **`DecideDrop` + `DropInput` + `DropAction`** in `client/dragdrop` + full table test (incl. the
   over-delete regression + precedence cases). Pure only, no wasm change yet. [centerpiece]
4. Rewire wasm through `DecideDrop` — snapshot inputs ONCE before teardown, switch on the verdict:
   - **4a.** `onMouseUp` (input.go) → commit; run electron.
   - **4b.** `commitRightClone` (right_button.go) → commit; reconcile/document canonical order; run electron.
   - **4c.** unify `onMouseMove` + `advanceCloneDrag` PREVIEW through `DecideDrop` (anti-divergence);
     run electron. [most invasive — last of the dragdrop work]
5. **`client/urlwalk`** — extract the boot descent walk from `applyURLOnBoot` (urlsync.go:144) as
   `Walk(state, gridLookup) (path, fileTileID)`; repoint; table-test missing-row skips, well
   boundary switch, content-leaf termination, wrong-grid avoidance. [Tier1.2, M]
6. **Shell refresh decision** — extract pure `DecideShellRefreshVisible(...)` from
   `shellRefreshButtonVisible` (shell_stream_client.go:95), keep probe side-effect in wasm; test. [Tier2, S]
7. **Embed descent validation** — extract the 3-gate validation/stash decision from
   `descendIntoEmbed` (embed.go:167) (decode href → tile found → same-grid v1 limit); keep the
   descent side-effect in wasm; test incl. cross-grid rejection. [Tier2, M]
8. **URL encode classification** — move the text-mode-vs-grid branching from `encodeFocusedPaneURL`
   (urlsync.go:90) into constructors on `client/url.State`; test. [Tier2, S–M]

Behavior PARITY is required at every wasm rewire — preserve exact current behavior, same helper
calls, just gathered-once-then-decided. After step 8, the side quest is done — resume the UX list.

---

## New pure API (client/dragdrop)

```go
type DropAction int
const (
    DropNavigate DropAction = iota // !started → bare click, navigation handled in wasm
    DropCreateTemplate             // template swatch drag → CreateTile
    DropPanEnd                     // tileID==0 → persist viewport
    DropDelete                     // over source pane's + (trashcan)  ← the regression branch
    DropEmbed                      // over raw-text descent → insert markdown reference
    DropRejected                   // read-only doc / forbidden / no target / same cell / occupied → snap back
    DropMove                       // left-drag → MoveTile
    DropClone                      // right-drag → CloneTile
)

// Snapshot gathered ONCE at release, before any teardown. No App fields, no js.Value.
type DropInput struct {
    Started    bool
    IsTemplate bool
    Clone      bool  // right-drag armed
    TileID     int64 // 0 = pan/template
    OverDelete bool  // cursor over origin pane's + button (computed via pure pointInPlus)
    OverDoc    bool  // raw-text descent under cursor
    DocReject  bool  // rendered (read-only) descent under cursor
    HasTarget  bool  // dropTargetAt resolved
    Forbidden  bool  // dropForbiddenForMove
    SameCell   bool  // drop cell == source cell
    Occupied   bool  // target cell already occupied
}

func DecideDrop(in DropInput) DropAction
```

**Canonical order inside `DecideDrop`** (mirrors current `onMouseUp`; reconciles the
slightly different `commitRightClone` order — delete and embed are mutually exclusive in
practice since the + button is in a grid-pane corner and a doc descent is a different
pane, so canonicalizing is behavior-preserving — DOCUMENT this in a comment):
1. `!Started` → `DropNavigate`
2. `IsTemplate` → `DropCreateTemplate`
3. `TileID == 0` → `DropPanEnd`
4. `OverDelete` → `DropDelete`
5. `OverDoc` → `DropEmbed`
6. `DocReject` → `DropRejected`
7. `!HasTarget` → `DropRejected`
8. `Forbidden` → `DropRejected`
9. `SameCell` → `DropRejected`
10. `Occupied` → `DropRejected`
11. else → `Clone ? DropClone : DropMove`

## Wasm rewire (the discipline that prevents the bug class)

`onMouseUp` (input.go ~724-870) and `commitRightClone` (right_button.go ~555-613): gather
**all** world-reads into a `DropInput` up front (call `a.overDeleteButton(d, …)`,
`a.docDropTargetAt`, `a.docRejectAt`, resolve the target via `a.dropTargetAt` once and
derive `HasTarget/Forbidden/SameCell/Occupied` via existing helpers
`dropForbiddenForMove`, `cellAtCursor`, `nodeAtCellInGrid`), THEN
`switch dragdrop.DecideDrop(in)` to execute side effects (`runDeleteTile`,
`commitEmbedDrop`, `MoveTile`/`CloneTile`, `cancelDragSnapBack`, pan save). All reads
happen before any `a.dragging = nil`, so a cleared field can never be read late.

`onMouseMove` (input.go ~625-686) and `advanceCloneDrag` (right_button.go ~459-498):
compute the SAME `DropInput` and call `DecideDrop`, mapping the action → ghost style
(`DropDelete`→shrink+fragment, `DropEmbed`→overDoc, `DropRejected`→forbidden/no-entry,
`DropMove/DropClone`→snap-to-cell, else→source size). **This is the anti-divergence win**:
preview and commit share one decision, so "preview shows delete, commit does move" becomes
structurally impossible.

## Test matrix (client/dragdrop_test.go, host-run by `make check`)

Table `DropInput → DropAction`:
- bare click (`Started:false`) → Navigate (beats everything)
- template → CreateTemplate
- pan (`TileID:0`) → PanEnd
- **over delete button → Delete** (the regression)
- delete + HasTarget + Occupied → Delete (precedence: delete wins over move/reject)
- over doc → Embed; doc reject → Rejected
- no target / forbidden / same cell / occupied → Rejected (one case each)
- clean left → Move; clean right (`Clone:true`) → Clone
- precedence: template beats delete; pan beats delete; etc.

(Optional) extract `OverDeleteButton` geometry into a pure predicate
`dragdrop.OverDeleteButton(originRect, cursor, started, tileID, originIsGrid) bool` (reuse
`palette.PointInPlus`) + geometry tests (in/out of radius, descended pane→false,
!started→false, tileID 0→false).

## Execution order — COMMIT EACH STEP SEPARATELY (user feedback: commit incrementally)

1. Add `DecideDrop`/`DropInput`/`DropAction` + full table test. Pure only, no wasm change. Green, commit.
2. Rewire `onMouseUp` (left) to snapshot→`DecideDrop`→switch. Verify parity. Commit.
3. Rewire `commitRightClone` (right); reconcile ordering with left; comment the canonical order. Commit.
4. Unify `onMouseMove` + `advanceCloneDrag` preview through `DecideDrop` (most invasive — last). Commit.
5. (Optional) extract `OverDeleteButton` geometry predicate + test. Commit.

Run `make check` every commit; run `make check-electron` after steps 2–4 (render/gesture
touched) — expect `HARNESS PASS` twice.

## Honest limits (state these; don't oversell)
- `DecideDrop`'s test locks in the precedence/ordering, but the ORIGINAL bug was upstream
  (`OverDelete` computed from a stale field). The unit test alone wouldn't reproduce that
  wiring bug — the **prevention is the snapshot discipline** (gather inputs once before
  teardown) + the **preview/commit unification**, not the table test per se.
- The impure resolvers (`dropTargetAt`, `docDropTargetAt`, `dropForbiddenForMove`,
  `nodeAtCellInGrid`, `tileAtCellInTarget`, and `overDeleteButton`'s pane-rect resolution)
  read `App.cache`/`tree` and stay in wasm, still unexecuted by `go test`. Pure-ifying them
  wholesale is diminishing returns — leave as thin feeders of `DropInput`.
- The js glue (action→RPC, DOM cursor, animation) stays uncovered by `go test`; only an
  end-to-end harness that actually boots the renderer (option A) exercises it. OUT OF SCOPE.

## Non-goals / within-reason cutoffs
- Don't extract navigation (descent/ascent on bare click) — separate, large.
- Don't build the e2e renderer harness (that's option A).
- Don't repoint cache-reading resolvers wholesale.

## Sibling extraction backlog (proactive audit, 2026-06-20)

Same testing gap (logic trapped in `//go:build js && wasm`, never executed by `go test`).
The drag-drop `DecideDrop` extraction above is the exemplar; these are the other candidates,
curated + verified (functions confirmed to exist at the cited lines):

TIER 1 — do (high ROI, low risk):
- **Text-cursor math** → new pkg `client/textcursor`. `offsetFromRowCol` (urlsync.go:333) is
  ALREADY a pure free func `(src string, row, col int) int` — just move it + add its inverse
  `RowColFromOffset`. Classic off-by-one/EOF/empty-line/CRLF hazards. Effort S. The reverse
  currently lives inside `textareaCursorRowCol` (urlsync.go:116) / `placeCursorAt` (:320) — lift
  the counting core out of the DOM method.
- **URL-restore walk** → new pkg `client/urlwalk`. The boot descent walk in `applyURLOnBoot`
  (urlsync.go:144-~257): resolve `state.TileIDs` into a grid descent, skip missing rows, switch
  at well boundaries, stop at content. Intricate state machine; a misstep lands you in the wrong
  grid. Extract as `Walk(state, gridLookup func(int64)(Grid,bool)) (path []int64, fileTileID int64)`.
  Effort M.

TIER 2 — worth it, more design than bug-risk:
- **Shell refresh-button decision** (shell_stream_client.go:95 `shellRefreshButtonVisible`): pull the
  pure 4-branch decision out of the probe side-effect → testable `DecideShellRefreshVisible(...)`. Effort S.
- **Embed descent validation** (embed.go:167 `descendIntoEmbed`): the 3 gates (decode href → tile
  found → same-grid v1 limit) + state-stash are pure-ish; extract the validation/stash decision,
  keep the descent side-effect in wasm. Effort M.
- **URL encode classification** (urlsync.go:90 `encodeFocusedPaneURL`): text-mode vs grid branching
  → constructors on `client/url.State`. Effort S–M.

EXISTING PURE PKGS — cheap test top-ups (happy-path only today, thin on malformed input):
- `client/url` (324 LOC): add malformed-decode cases (bad ids, bad floats, truncated paths).
- `client/shellconn` (54 LOC): edge cases beyond the close-code enum.

LEAVE ALONE (genuine glue, not extractable): glyphs.go, gesture_draw.go, palette_draw.go,
markdown_render.go, render.go painters, touch.go, url_modal.go, url_preview.go, webview_bridge.go,
mutate.go (already uses tested clientsync.Classify), file_overlay.go (panebox adapters),
drop_target.go (dragdrop/zoomtrans/pane adapters), main.go.

## Context: this is a SIDE QUEST
Main thread is the user's 7-item UX list. Status (2026-06-20): #2 (divider hover cursor)
and #6 (blackhole→trashcan delete) DONE; #1 (terminal scroll) DEFERRED; **#3 (right-click
links in browser — mid-discussion), #4 (GitLab draft comments lost / cookies), #5 (save
markdown while typing), #7 (text previews fill vertically) STILL PENDING.** After this
extraction, resume that list.
