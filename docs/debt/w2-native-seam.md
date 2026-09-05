# W2 — The native seam sheds its timing heuristics

The focus-steal guard in `apps/desktop/src/main/webviews.ts` is the one
mechanism in Gridwell that decides by racing a clock. This document says what
the clock is standing in for, whether an owned fact can take its place, and
what the extraction and the test-gap closure are either way.

## 1. What the guard did before this workstream

`bounceStolenFocus` in `wireNav`, bound to the view's `webContents` `focus`
event: return if `e.focused`; return if `Date.now() - e.lastUserClickMs <
USER_CLICK_FOCUS_GRACE_MS` (1500); otherwise report `onFocusStolen` — `index.ts`
calls `rootWC.focus()` — and schedule a recheck at `FOCUS_RECHECK_MS` (120)
that bounces again if the pane is still unfocused, the view still holds OS
focus, and the stamp is still stale.

`e.focused` is an owned fact: the renderer writes it on `place`
(`PlaceArgs.focused`) and on every `setHidden` (`syncURLViews`, `paneID ==
a.tree.Focus`). `e.lastUserClickMs` was the heuristic — a timestamp compared
against a constant — and §5 measured it wrong on every first click. It is gone;
§3 and §4 say what replaced it.

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
  **Measured (§5, M1): step 2 precedes step 4, 8/8.** The 1500 ms is not doing
  what its comment claims: it covers the *second* click and the recheck, not
  the first.
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
1500 ms works, because the stamp is fresh by then. That was a falsifiable
prediction; §5 M1 settles it — the bounce is real on every first click, and on
Linux the damage is masked by the click's own widget-focus commit landing
after the bounce and winning. The report is wrong either way, and the mask is
the platform's accident, not the guard's.

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
Electron (`electron.d.ts`, `InputEvent.type` includes `'mouseDown'`,
`'touchStart'` and `'pointerDown'`). It fires in the browser process when an
input event is sent to that WebContents — no renderer round trip, no IPC. It is
the same fact the preload forwards, obtained one process earlier, and it can
replace `lastUserClickMs` outright: both `input-event` and `focus` are emitted
from B, so their order is deterministic rather than racy.

M2 measured that order, and it is the opposite of the hoped-for one: `focus`
fires **first**, `input-event`/`mouseDown` 0.1–3.0 ms later, 8/8. Chromium
focuses the widget in the browser process while routing the press, before
forwarding the press to the renderer. So no fact — not the stamp, not
`input-event`, not an ack — exists yet at the instant the `focus` event fires.
The guard cannot be causal *backwards*. What it can be is causal *forwards*:
do not decide in the focus handler at all, and one settle later ask whether a
press into this view arrived after this focus. `input-event` is the right
observer for that question because it is main's own — a wedged page delays the
preload's IPC stamp arbitrarily, and cannot delay the input router.

One caveat holds: `sendInputEvent` from main also emits `input-event`, so the
correlation must be typed to press-shaped events only — `touchScroll` injects
`mouseWheel`, and nothing else in the tree synthesizes a press.

For the post-bounce commit (`FOCUS_RECHECK_MS`), the candidate owned fact is
the root webContents' `blur`. M3 measured it and it fails: `root:blur` fires
0.2 ms *before* the view's `focus`, as part of the same focus move, so it is
the steal itself and not a signal that a later commit overrode the bounce. M3
also measured something more useful: an immediate `rootWC.focus()` from inside
the focus handler never takes — the in-flight widget-focus commit wins — and
the bounce at +121 ms is the only one that ever lands.

### Verdict: REPLACEABLE, by deferring rather than by correlating backwards

- **Intent** ("was this focus the user's?") becomes an owned native fact:
  a press-shaped `input-event` on the same view, in the same process, counted
  and compared against the count snapshotted at the `focus` event. No clock,
  no window, no `USER_CLICK_FOCUS_GRACE_MS`.
- **Ack** ("has the renderer agreed?") is not needed. The renderer's
  `focused` is already read at the settle; the press correlation answers the
  intent question a round trip earlier, so no deadline on the renderer is
  introduced.
- **Commit** (`FOCUS_RECHECK_MS`) survives, renamed to the settle it always
  was, and is **documented as unavoidable with M3 quoted**: Chromium emits no
  event for a widget-focus commit that lands after ours, and an immediate
  bounce is swallowed by it. The one constant left in the guard is how long
  main waits for that commit — never a decision about whose focus it is.

No constant is deleted on faith; each survives only with a measurement on
record.

## 4. The extraction

`apps/desktop/src/main/focusguard.ts`, a js-free-of-Electron pure module in the
`viewutil.ts` shape: the production constant exported from the module so the
test pins the real value, no Electron imports, one decision function. This is
what landed, after §5 replaced the timestamp with the press count:

```ts
export const FOCUS_SETTLE_MS = 120;

export function isPressInput(type: string): boolean; // mouseDown/touchStart/pointerDown

export type GuardPhase = 'focus-event' | 'settle';

export interface GuardInput {
  phase: GuardPhase;
  paneFocused: boolean;      // Entry.focused — the renderer's owned fact
  viewHoldsOSFocus: boolean; // webContents.isFocused(); true at 'focus-event'
  pressesAtFocus: number;    // the view's press count when the grab happened
  pressesNow: number;        // the count now; a rise is the user's click
  alreadyBounced: boolean;
}

export type GuardAction =
  | { kind: 'allow' }
  | { kind: 'wait'; settleMs: number }
  | { kind: 'bounce'; settleMs: number | null };

export function decideFocus(i: GuardInput): GuardAction;
```

There is no timestamp in the input at all, so the "clock stepped back" case has
nowhere to arise: a monotonic count answers "did a press land after this
focus?" exactly, where two timestamps only approximate it.

`webviews.ts` keeps only the executor: count presses from `input-event`,
subscribe to `focus`, read `isFocused()` inside the try/catch, call
`cb.onFocusStolen`, schedule and cancel the timer, and the
`this.entries.get(paneId) !== e` identity check. No comparison, no constant, no
branch on time lives there.

`focusguard.test.ts`, table-driven under `node --test` like `viewutil.test.ts`:

| # | case | expect |
|---|---|---|
| 1 | focus-event, paneFocused | allow |
| 2 | focus-event, unfocused, no press yet | wait `FOCUS_SETTLE_MS` |
| 3 | settle, unfocused, holds focus, no press | bounce + settle |
| 4 | settle, `alreadyBounced` | bounce, `settleMs: null` |
| 5 | settle, pane became focused meanwhile | allow, no second bounce |
| 6 | settle, view no longer holds OS focus | allow |
| 7 | settle, a press landed since the grab | allow |
| 8 | focus-event, a press already counted (press-first host) | allow |
| 9 | settle, an equal non-zero count (an older, consumed click) | bounce |
| 10 | settle after a bounce, a press landed meanwhile | allow |
| — | every case decided twice | identical (no clock) |

Case 10 is the one the old code got right by accident: the recheck arm re-read
`lastUserClickMs`, so a press arriving during the 120 ms suppressed the second
bounce. A naive extraction would have dropped it silently. Case 9 is the one it
got wrong: a 1500 ms grace treats any recent click as consent for every focus
grab that follows it, and a count does not.

## 5. Measurements

These are harness scenarios, not code changes, and they gate §3's conclusions.

- **M1 — `focus` vs `VIEW.leftdown`.** Place a view on an unfocused pane, press
  left into it, and record the arrival order and delta of the `focus` event and
  the `noteUserClick` stamp. Decides whether the first click into an unfocused
  live pane is bounced today.
- **M2 — `input-event` vs `focus`.** The same press, recording `input-event`
  against `focus`.
- **M3 — root `blur` after a bounce.** Force a steal, call `rootWC.focus()`,
  and record whether the delayed widget-focus commit blurs the root
  webContents.

### Measured

Run 2026-09-05 on the Linux dev box (Electron 43, user-space Xvfb, one
throwaway harness deleted after the run). The press had to be a **real OS
click**: `sendInputEvent` injects straight into the renderer's input router and
bypasses Chromium's browser-process focus routing, so a synthetic press emits
`input-event` and the preload's IPC stamp but **no `focus` event at all** and
leaves `isFocused()` false (4/4). The box has no xdotool, so the click was
delivered as an XTEST `FakeInput` over the raw X protocol on the Xvfb socket.
That detail matters for anyone rerunning this: a harness that only calls
`sendInputEvent` cannot see M1's ordering.

**M1 — the first click into an unfocused live pane IS bounced. 8/8.** Times in
ms from the press, one representative trial, and the order was identical in all
eight:

| event | where | t |
|---|---|---|
| root `blur` | B | 104.2 |
| `onFocusStolen` (the guard fires) | B | 104.3 |
| view `focus` | B | 104.4 |
| `input-event` `mouseDown` | B | 104.5 |
| `noteUserClick` (`VIEW.leftdown` IPC) | B | 105.1 |

`focus` → `input-event` was 0.1–3.0 ms across the eight; `focus` →
`noteUserClick` was 0.7–3.8 ms. The stamp is always later than the focus event,
so the guard always reads a stale stamp on a first click and always reports a
steal for a click the user made.

Its user-visible cost on Linux is nil today, and by accident: `isFocused()` was
still true 1.2 s after every one of the eight bounces. The `rootWC.focus()` the
bounce triggers is swallowed by the click's own in-flight widget-focus commit,
and the recheck 120 ms later then allows the view because the stamp has arrived
by then. Two wrongs. On a host where the immediate `rootWC.focus()` does take,
nothing re-focuses the view and the user's click loses keyboard focus for good.

**M2 — NEGATIVE. `input-event` does not precede `focus`; it follows it** by
0.1–3.0 ms, 8/8 (rows 3 and 4 above). Chromium focuses the widget in the
browser process while routing the press and forwards the press afterwards. A
guard that decides inside the focus handler is blind whatever fact it reads.
What M2 does establish is that the press reaches **main** 0.6–0.9 ms before the
renderer's IPC stamp does, on a fact main owns and page JS cannot delay.

**M3 — NEGATIVE as a replacement, decisive as a justification.** `root:blur`
fired 0.2 ms *before* the view's `focus`, i.e. as the first half of the same
focus move, not as a later commit overriding a bounce — it cannot distinguish
the two. And the immediate bounce never took: forcing a steal with `wc.focus()`
produced `onFocusStolen` at +0.2 ms with no view `blur` and no `root:focus`
following it; the second bounce, from the recheck at +121 ms, is the one that
moved focus back (view `blur` +121.2, `root:focus` +121.3). So
`FOCUS_RECHECK_MS` is not an optimisation over an immediate bounce — it is the
only bounce that works, and it stays.

**What the three together say.** The window §2 asks the clock to cover cannot
be covered *at the focus event*, because at that instant nothing has happened
yet but the focus. It can be covered *one settle later*, by a fact main owns:
did a press land in this view after this focus? That is the design in §4, and
it deletes `USER_CLICK_FOCUS_GRACE_MS` rather than shortening it.

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
