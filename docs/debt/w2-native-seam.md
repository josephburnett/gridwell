# W2 — The native seam sheds its timing heuristics

The focus-steal guard in `apps/desktop/src/main/webviews.ts` is the one
mechanism in Gridwell that decides by racing a clock. This document says what
the clock is standing in for, whether an owned fact can take its place, and
what the extraction and the test-gap closure are either way.

## 1. What the guard does today

`bounceStolenFocus` in `wireNav`, bound to the view's `webContents` `focus`
event: return if `e.focused`; return if `Date.now() - e.lastUserClickMs <
USER_CLICK_FOCUS_GRACE_MS` (1500); otherwise report `onFocusStolen` — `index.ts`
calls `rootWC.focus()` — and schedule a recheck at `FOCUS_RECHECK_MS` (120)
that bounces again if the pane is still unfocused, the view still holds OS
focus, and the stamp is still stale.

`e.focused` is an owned fact: the renderer writes it on `place`
(`PlaceArgs.focused`) and on every `setHidden` (`syncURLViews`, `paneID ==
a.tree.Focus`). `e.lastUserClickMs` is the heuristic — a timestamp compared
against a constant.

## 2. The race, precisely

Three processes. B = the Electron browser (main) process, V = the live view's
renderer, R = the app renderer (wasm).

A legitimate click into an **unfocused** live pane:

| # | where | step |
|---|---|---|
| 1 | B | user presses left over view V of pane P; Chromium routes the press and focuses V's widget |
| 2 | B | `webContents` `focus` emits on V → `bounceStolenFocus` runs with `e.focused === false` |
| 3 | V | the press reaches V's renderer; `urlview-preload.ts` mousedown capture sends `VIEW.leftdown` |
| 4 | B | `register.ts` `ipcMain.on(VIEW.leftdown)` → `registry.noteUserClick` stamps `lastUserClickMs`; relays `EV.leftForward` |
| 5 | R | `onForwardedLeftDown` → `focusToPane(P)` → focus changed → `draw()` |
| 6 | R | `syncURLViews` → `bridgeSetHidden(P, hidden, true)` |
| 7 | B | `registry.setHidden` sets `e.focused = true` |

A page-initiated steal (`location.reload()` on an unfocused pane) is steps 1–2
with no 3–7 at all: no input, no ack.

**The window the clock must cover is 2 → 7.** Three sub-windows, and they are
not the same kind of thing:

- **2 → 4 (focus before stamp).** Step 2 happens in B during input routing.
  Step 4 needs a round trip out to V's renderer and back over IPC. If step 2
  precedes step 4, then at the moment the guard runs the stamp is *stale*, and
  the guard bounces the user's own click. The `lastUserClickMs` check as
  written is backward-looking; it cannot see a press that has not arrived yet.
  A renderer ack cannot help here either — it is even further behind.
  **This ordering is unmeasured and must be measured (§5, M1).** If step 2
  precedes step 4, the 1500 ms is not doing what its comment claims: it is
  covering the *second* click and the recheck, not the first.
- **4 → 7 (stamp before ack).** A genuine ack window: main knows a press
  landed and is waiting for the renderer to agree that the pane is focused.
  A clock here can be replaced by the ack itself, with a deadline (§3).
- **after 7.** Chromium's widget-focus commit can land after the bounce with
  no further `focus` event. This is what `FOCUS_RECHECK_MS` covers.

`onFocusStolen` only calls `rootWC.focus()`, which moves **OS keyboard focus**.
Pane focus is a separate, renderer-owned fact and is unaffected. So a wrong
bounce does not break `live-view-focus.spec.ts` (which asserts pane focus). Its
user-visible cost is narrower and untested: **you click into a text field in a
live page and your keystrokes go to the canvas instead.** A second click within
1500 ms works, because the stamp is fresh by then. That is a falsifiable
prediction, and §5 M1 settles it.

## 3. Can an owned fact replace the clock?

### (a) An explicit renderer focus-ack

The proposal: on `focus` with `e.focused === false` and a pending user click,
do not bounce; wait for the first `setHidden`/`place` carrying `focused === true`
for that pane, and treat that as the close of the window.

It is well-defined. `focusToPane` draws only when focus actually changed — but
the no-change case is exactly the case where `e.focused` was already true and
the guard returned at its first line. When the guard is reached, an ack is
coming.

Two things it does not fix. It does not cover 2 → 4: an ack is strictly later
than the stamp, so if the guard runs before the stamp there is no pending click
to defer on. And it needs a deadline, because main cannot know the renderer
will ever answer — the wasm may decline to focus that pane (a parked stacked
level is not focusable), the pane may close mid-flight, the renderer may wedge.
An unbounded wait leaves a page holding keyboard focus forever, the failure the
guard exists to prevent.

So the ack converts a correctness heuristic into a **liveness timeout**: the
constant stops deciding whether the bounce is right and decides only how long
main tolerates silence. Worth doing; not on its own a removal.

### (b) An Electron-native fact

`webContents.on('input-event', (_e, inputEvent) => …)` exists in the pinned
Electron (`electron.d.ts`, `InputEvent.type` includes `'mouseDown'`). It fires
in the browser process when an input event is sent to that WebContents — no
renderer round trip, no IPC. It is the same fact the preload forwards, obtained
one process earlier, and it can replace `lastUserClickMs` outright: both
`input-event` and `focus` are emitted from B, so their order is deterministic
rather than racy. If `input-event` precedes `focus` (§5, M2) the guard becomes
causal — *this focus followed a mouseDown into this view* — with no window and
no constant, and a `focus` with no preceding `mouseDown` is provably a steal,
bounced immediately.

Two caveats to pin in the measurement. `sendInputEvent` from main also emits
`input-event`, so the correlation must be typed to `mouseDown` only —
`touchScroll` injects `mouseWheel` and nothing else in the tree synthesizes a
press. And a Tab into the view, or a page `.focus()` after a click elsewhere,
still wants an upper bound on the correlation; but that bound is a staleness
bound on a fact main owns, not a guess at another process's latency.

For the post-bounce commit (`FOCUS_RECHECK_MS`), the candidate owned fact is
the root webContents' `blur`. A commit that lands after `rootWC.focus()` blurs
the root; if at that moment no entry has `focused === true` and some entry's
`webContents.isFocused()`, that is the same steal, detected by an event instead
of a poll. Not an assumption — `rootWC` also blurs legitimately when the user
clicks into the focused pane's view — so it is measurement M3.

### Verdict: PARTIALLY REPLACEABLE

- **Intent** ("was this focus the user's?") becomes an owned native fact:
  `input-event`/`mouseDown` correlation on the same view, in the same process,
  deterministically ordered — pending M2. That removes
  `USER_CLICK_FOCUS_GRACE_MS` as a correctness heuristic.
- **Ack** ("has the renderer agreed?") becomes an explicit ack with a bounded
  deadline. The bound is irreducible and must be **documented as unavoidable**:
  main cannot distinguish a slow renderer from one that will never answer, and
  the safe default on silence is to hand focus back.
- **Commit** (`FOCUS_RECHECK_MS`) reduces to the root-blur fact if M3 holds;
  otherwise it stays, documented as unavoidable, because Chromium emits no
  second `focus` for a commit that lands after ours.

No constant is deleted on faith; each survives only with a measurement on
record.

## 4. The extraction (independent of §3)

`apps/desktop/src/main/focusguard.ts`, a js-free-of-Electron pure module in the
`viewutil.ts` shape: production constants exported from the module so the test
pins the real values, no Electron imports, one decision function.

```ts
export const USER_CLICK_FOCUS_GRACE_MS = 1500;
export const FOCUS_RECHECK_MS = 120;

export type GuardPhase = 'focus-event' | 'recheck';

export interface GuardInput {
  phase: GuardPhase;
  paneFocused: boolean;      // Entry.focused — the renderer's owned fact
  viewHoldsOSFocus: boolean; // webContents.isFocused(); true at 'focus-event'
  lastUserInputMs: number;   // the intent stamp; 0 = never
  now: number;
  ackSeen?: boolean;         // renderer confirmed focused=true since the stamp
}

export type GuardAction =
  | { kind: 'allow' }
  | { kind: 'bounce'; scheduleRecheckMs: number | null };

export function decideFocus(i: GuardInput): GuardAction;
```

`webviews.ts` keeps only the executor: subscribe to `focus`, read `isFocused()`
inside the existing try/catch, call `cb.onFocusStolen`, schedule and cancel the
timer, and the `this.entries.get(paneId) !== e` identity check. No comparison,
no constant, no branch on time lives there.

`focusguard.test.ts`, table-driven under `node --test` like `viewutil.test.ts`:

| # | case | expect |
|---|---|---|
| 1 | focus-event, paneFocused | allow |
| 2 | focus-event, unfocused, `lastUserInputMs === 0` | bounce + recheck |
| 3 | focus-event, unfocused, input 100 ms ago | allow |
| 4 | focus-event, unfocused, input 1499 ms ago | allow (boundary) |
| 5 | focus-event, unfocused, input exactly 1500 ms ago | bounce (boundary; `>=` bounces) |
| 6 | recheck, pane became focused meanwhile | allow, no second bounce |
| 7 | recheck, view no longer holds OS focus | allow |
| 8 | recheck, still unfocused + still holds focus + stale stamp | bounce, `scheduleRecheckMs: null` |
| 9 | recheck, a click landed between bounce and recheck | allow |
| 10 | `now < lastUserInputMs` (clock stepped back) | allow, documented |
| 11 | ackSeen true with a stale stamp | allow (phase 2) |
| 12 | ackSeen false past the deadline | bounce (phase 2) |

Case 9 is the one the current code gets right by accident: the recheck arm
re-reads `lastUserClickMs`, so a press arriving during the 120 ms suppresses
the second bounce. A naive extraction drops it silently. It is untested today.

## 5. Measurements

These are harness scenarios, not code changes, and they gate §3's conclusions.
Each runs in `capture-harness.ts` (which already drives the registry directly
and already has a focus-steal scenario) under `make check-electron`.

- **M1 — `focus` vs `VIEW.leftdown`.** Place a view on an unfocused pane
  through the real IPC path (`bridge-harness.ts`), synthesize a left
  `mouseDown` with `sendInputEvent`, and record the arrival order and delta of
  the `focus` event and the `noteUserClick` stamp. Decides whether the first
  click into an unfocused live pane is bounced today.
- **M2 — `input-event` vs `focus`.** The same press, recording `input-event`
  (`type === 'mouseDown'`) against `focus`. If `input-event` precedes `focus`
  deterministically over many presses, the grace constant becomes a causal
  check.
- **M3 — root `blur` after a bounce.** Force a steal, call `rootWC.focus()`,
  and record whether the delayed widget-focus commit blurs the root
  webContents. If it does, `FOCUS_RECHECK_MS` becomes an event.

Record each result here. A constant that survives gets its measurement quoted
in its comment: the lens asks that a remaining heuristic be documented as
unavoidable, and a measurement is what makes that documentation true rather
than asserted.

## 6. `webviews.ts` path audit

CLAUDE.md: "`webviews.ts` has no direct test; anything you touch there gets
one." The table is the audit as of this document. Covering tests are named by
file; GAP means nothing exercises the path.

| # | path | covered by |
|---|---|---|
| 1 | `place` happy path (view, bounds, addChildView) | `capture-harness.ts` scenario 1; `e2e/palette-under-webview.spec.ts`; `e2e/user-agent.spec.ts` |
| 2 | `place` replace-without-close arm (stale entry → `onError` + `remove`) | **GAP** — nothing places twice into one paneId |
| 3 | `place` `startHidden` parked bounds | `e2e/palette-under-webview.spec.ts` "a new view placed with hidden=true starts parked"; `capture-harness.ts` |
| 4 | `place` `focused` from `PlaceArgs` reaching the entry | `capture-harness.ts` place-focus scenario (both arms); `bridge-harness.ts` across the real IPC seam |
| 5 | `place` `reviveNavigation` restore vs load | `e2e/url-history.spec.ts` (restore); `viewutil.test.ts` "the edited address beats a stale back-stack" |
| 6 | `setBounds` — parked view not lifted | `e2e/palette-under-webview.spec.ts` "setBounds() while hidden keeps the view parked" |
| 6b | `setBounds` — early return on equal bounds | **GAP** — `boundsEqual` is unit-tested; the early return is not |
| 7 | `setHidden` park / unpark | `e2e/palette-under-webview.spec.ts`; `e2e/url-modal.spec.ts`; `e2e/workspace-live.spec.ts`; `capture-harness.ts` |
| 7b | `setHidden` `focused`-only change tracked on the entry | **GAP** — focus tracking is only covered on the `place` path |
| 8 | `remove` freeze payload (jpeg / url / title / history) | `capture-harness.ts`; `bridge-harness.ts`; `e2e/url-history.spec.ts`; `e2e/auto-live.spec.ts` |
| 9 | `remove` crash arm | PARTIAL — `capture-harness.ts` dead-view scenario proves the catch is entered and the freeze is empty, but its `onError` text is unasserted (the harness passes `{}` callbacks) |
| 10 | `remove` detach-failure arm (`failed to detach live view`) | **GAP** |
| 11 | `remove` `flushStorageData` / localStorage survival | **GAP** — the largest documented-but-untested claim in the file |
| 12 | `remove` cancelling the pending `focusRecheck` | **GAP** |
| 13 | `capture` failing streak → `onError` | **GAP** |
| 14 | `capture` recovered → `onError` | **GAP** |
| 15 | context menu, in-page right-click door | `e2e/url-context-menu.spec.ts` (both tests) |
| 16 | context menu, `showMenu` bar-circle door | `e2e/url-circle-menu.spec.ts`; `bridge-harness.ts` |
| 17 | focus announce before popup | `bridge-harness.ts` (order assertion); `e2e/context-menu-focus.spec.ts` |
| 18 | `canFreeze` / `durable` gating | PARTIAL — `contextmenu.test.ts` covers the template; `url-circle-menu.spec.ts` the durable positive; the ephemeral negative at the registry is uncovered |
| 19 | zoom chord relay + `zoomChordRelays` | `e2e/content-zoom.spec.ts`; `viewutil.test.ts` `zoomChordKey` |
| 20 | F11 fullscreen relay | **GAP** — and its canvas sibling in `window.ts` is untested too |
| 21 | `setWindowOpenHandler` / `openBelowUrl` filter | `e2e/open-below.spec.ts` (both arms); `e2e/stacked-visit-orphan.spec.ts`; `viewutil.test.ts` |
| 22 | `touchScroll` injection (sign + coords) | `capture-harness.ts` touch scenario, which pins the direction separately from "did nothing" |
| 23 | nav events | PARTIAL — `capture-harness.ts` asserts a nav event with the right tileId and title; `did-navigate-in-page` specifically is uncovered |
| 24 | `did-fail-load` filter → `onError` | `e2e/errsurface.spec.ts`; `viewutil.test.ts` filter + message |
| 25 | `render-process-gone` → `onError` | **GAP** at the integration level — only `renderProcessGoneMessage` is unit-tested; the `getURL()` try/catch after a crash is untested |
| 26 | focus steal: bounce on `focus` when unfocused | `e2e/url-focus-steal.spec.ts`; `capture-harness.ts` place-focus scenario |
| 27 | focus steal: the click grace suppressing a bounce | **GAP** — the leftdown relay and `noteUserClick` do run in `e2e/live-view-focus.spec.ts`, but nothing asserts the grace suppresses a bounce |
| 28 | focus steal: the `FOCUS_RECHECK_MS` arm | **GAP** — `url-focus-steal.spec.ts` passes on the first bounce alone |
| 29 | focus steal: view died under the recheck timer | **GAP** — the exact hang the code's comment warns about |
| 30 | `applyMinWidthZoom` / `setZoom` composition | `e2e/content-zoom.spec.ts`; `viewutil.test.ts` (three units). The `setZoomFactor` try/catch and the `did-finish-load` re-apply are not separately asserted |
| 31 | `goBack`, including the no-op at the start of history | `capture-harness.ts`; `contextmenu.test.ts` enablement flags |

**Gap count: 13 GAP rows — 2, 6b, 7b, 10, 11, 12, 13, 14, 20, 25, 27, 28, 29 —
plus 4 partials: 9, 18, 23, 30.** Rows 6b and 7b are split out of items 6 and 7
because each has a covered half and an uncovered half.

Every gap except 20 is an error or edge arm — the class this file's own
comments argue hardest for ("must be loud", "must not fail silently", "hangs
main behind an error dialog"). That is the shape of the deficiency: the happy
paths are well covered and the failure arms are not.

## 7. Phases

### Phase 1 — extraction + unit tests

`focusguard.ts` + `focusguard.test.ts` (§4), `webviews.ts` reduced to the
executor. Pure refactor: no behavior change, no constant changed.

**Done bar.** `make check` green (the `node --test` suite picks the new file up
from `src/**/*.test.ts`). `make check-electron` and `make check-e2e` green —
this is the native layer, so `make check` proves nothing on its own. Every case
in the §4 table present, including 9 and 10. No time comparison and no constant
left in `webviews.ts`. `deadcode.test.ts` still green (it catches unused
exports).

### Phase 2 — measurements and harness coverage for steal / bounce / recheck

Run M1–M3 and write the results into §5. Then:

- Add the harness scenarios for the paths §6 marks GAP in the focus family:
  the grace window (`noteUserClick` then `focus` → no bounce), the recheck arm
  (bounce, then a delayed re-grab → second bounce), the click-during-recheck
  suppression, and the die-under-timer arm (remove the view between bounce and
  recheck → no throw, no bounce).
- Replace the `entries` cast in `capture-harness.ts` with a test-only accessor
  beside `viewBoundsFor` / `focusedFor`. A harness reaching through `private`
  is a seam that can rot silently.
- If M2 holds, land the `input-event` correlation as its own commit, with the
  measurement in the commit message and the constant deleted or re-scoped. If
  M3 holds, land the root-blur replacement likewise. If either fails, write the
  failure into §5 and leave the constant with the measurement in its comment.

**Done bar.** Each focus path in §6 has a named covering test. `make
check-electron` and `make check-e2e` green. Every surviving constant has a
measurement quoted beside it.

### Phase 3 — gap closure

Close the remaining §6 GAPs, one commit per path, cheapest surface first: a
pure function extracted and unit-tested where the path is a decision, a
`capture-harness` scenario where it is registry behavior, an e2e spec where it
is user-visible. Prefer the harness: it drives the registry directly and is
cheaper and steadier than Playwright.

**Done bar.** No GAP rows left in §6, or a row explaining why the path is not
reachable from a test with the cost written down. `make check-electron` and
`make check-e2e` green. `docs/debt-program.md`'s W2 status ticked in the same
commit as the last phase.

## 8. Running the gates on this dev box

No document in `docs/` describes this; the only trace is
`.github/workflows/gates.yml`, which sets `ELECTRON_DISABLE_SANDBOX=1` with the
comment "same switch the dev box uses under its user-namespace xvfb". This
section is that missing note.

The box has no root, no system xvfb and no system chromium. Substitute for
`xvfb-run -a`:

```
ELECTRON_DISABLE_SANDBOX=1 ~/.local/electron-libs/with-xvfb.sh <cmd>
```

`make check-electron` runs `npm run test:integration && npm run test:bridge`,
whose npm scripts wrap `xvfb-run` themselves, so run the underlying `electron
dist/harness/*.js` through the wrapper rather than the make target.
`make check-e2e` is `npm run build && xvfb-run -a npm run test:e2e`.

Both gates rebuild at start, so never edit sources while one runs. A
single-spec Playwright run rebuilds nothing: run `make build && (cd
apps/desktop && npm run build)` first, or the spec tests last week's binaries.
Long e2e batches wedge intermittently when launched as background shells here;
run foreground chunks and treat CI as the arbiter.
