# Gridwell Stabilization Plan

A staged plan to convert the project from "fixes don't stay fixed" to
rock-solid. It is derived from a four-front analysis (git history, the
conversation record, the architecture, and the test suite) that all pointed at
**one root cause**:

> A single fact (where you are, how it is framed, whether the menu is open,
> whether a control shows) is held in **several parallel copies with no single
> owner**, written from many code paths. A fix corrects one copy; another path
> keeps writing the rest; the bug returns. The recurring symptoms — menus
> disappearing, previews going wonky, content vanishing, controls on the wrong
> pane — are all this one disease at different seams.

The store and server are **sound by construction** (`ARCHITECTURE.md §3–4`).
All fragility is in the **client/native split** (`§5–6`), and the cure already
exists in the codebase four times (`§7`): *derive a fact once, read it
everywhere.* This plan applies that cure to the seams in `§8`, and builds the
test homes the fragile invariants (`§11`, I7–I11) currently lack.

**Sequencing principle.** Start where symptom-value is high and effort is low to
prove the pattern (the menu), then take the hardest/highest-value seam
(framing), and run the make-it-testable extraction alongside. Each phase ends
green on all three gates; nothing in a later phase starts until the earlier
seam has a test that crosses it.

---

## Phase 0 — Guardrails (this session; ~done)

The documentation that should have existed. Low effort, prevents the next agent
from re-creating the disease.

- [x] **`ARCHITECTURE.md`** — the verified layer map, invariant inventory, seam
      catalog, and the cure pattern.
- [x] **`CLAUDE.md` rewrite** — the engineering charter (one fact/one owner;
      root-cause before patching; test-the-seam; the three gates) plus the
      preserved domain doctrine.
- [ ] **Fix the stale comments now** (`§9`): rename `internal/store/cow.go` →
      `clone.go`/`path.go`; correct the `loader.go` "in-process" and `config.go`
      "Attach" comments and the `Path` proto "COW spine" comment. Pure
      doc/rename, no behavior change — do it in one commit so no future change is
      misled. **Recommended first commit.**

---

## Phase 1 — Give every fragile fact a single owner (the core)

This is the heart of the work. Each item collapses a seam from `§8` to one
owner, with the test that proves it. Do them as separate, independently
shippable commits.

### 1a. The menu state machine → one owner  *(start here: cheap, high symptom-value)*
**Disease:** 14 imperative `a.menuOpen = …` sites, 11 of them `= false`, no
`closeMenu()`/`openMenu()`; a `MenuOpen` bool also threaded through portal
frames. Any new gesture-end path that forgets its assignment ⇒ "menu disappears
/ wrong pane."
**Cure:**
- Extract a `js`-free `client/menu` (or fold into `client/pane`) holding the
  menu state and the *one* set of transitions: `Open(paneID)`, `Close()`, and
  the focus/ascent rules. The wasm layer calls these; it never assigns the bool.
- Reduce the copies: the portal-frame `MenuOpen` either derives from the owner or
  is removed.
**Tests (unit, headless):** menu closes on focus change; menu is gone/restored
correctly across ascent/descent; opening on pane A then acting on pane B leaves
exactly one consistent menu state.
**Gate:** `make check` + an e2e spec asserting menu-close-on-focus over a live
view (extend the existing palette spec, which today only checks `open===true`).

### 1b. Viewport / framing → collapse five copies to one  *(highest value; hardest)*
**Disease:** framing lives in 5 places (`pane.Pane.{Anchor,Path,Cx,Cy,Zoom}`,
`App.paneStateStack`, server `view_*`, `pane.Up` portal frames, the URL) that
must agree for the descend/ascend round-trip to be idempotent; they desync on
pane close and on SSE-during-animation ⇒ "preview goes wonky."
**Cure:**
- Define **one owned `Frame` representation** per pane (anchor + center + intrinsic
  zoom). The server `view_*` is its persistence; the URL and the ascent stack are
  **derived projections**, not independent stores.
- Make descend/ascend a pure transition over `Frame` in a testable package
  (`client/pane` already owns the stack primitive `TestPortalStackRoundTrip`;
  move the *caller's* decision logic — currently in `input.go` — down beside it).
- Reconcile inbound SSE against the owned `Frame` with a version interlock, so an
  event during an animation cannot mutate what you're looking at.
**Tests:** descend → reframe → ascend yields a byte-identical `Frame`; an SSE
event mid-animation does not change a non-edited pane; pane close→reopen restores
the exact `Frame` (the orphaning bug).

### 1c. Shared rule, shared constant  *(low effort, removes drift)*
**Disease:** "controls show only on the focused pane" is encoded on the canvas
(Go) *and* as `controlVisible` (TS); the drag threshold is `dragThreshold` (Go)
*and* `RIGHT_DRAG_THRESHOLD = 4` (TS).
**Cure:** one source for each. Either generate the TS constant from the Go source
(or vice-versa) or document a single owner and add a test asserting the two
agree. The focused-pane-controls predicate should have one definition both layers
consult.
**Tests:** a check that the Go and TS values match; the predicate truth table
(already exists for `controlVisible`) plus a test that the **caller** feeds it the
right `(parked, focused)` on a real focus change.

---

## Phase 2 — Make the epicenter testable

The reason the §1 work is even possible safely. `client/wasm` (10,660 LOC, 0
tests) and the native layer must stop being black boxes.

- **Dissolve the `App` god-object.** Per-pane state currently lives in ~12 maps
  keyed by pane id that orphan on close. Replace with one owned per-pane struct
  with a clear lifecycle (created/destroyed atomically), so no map can hold a
  stale id. Extract the event→state→redraw decisions into pure reducers in
  `client/*` packages and unit-test them; the wasm file becomes glue.
- **Extract `webviews.ts` logic.** Pull the bounds/clip/park/teardown decisions
  out of the 444-LOC registry into pure modules (like `viewutil.ts`) with unit
  tests, and cover the registry itself under `make check-electron`.
- **Establish the rule going forward** (now in `CLAUDE.md §5`): new behavior in
  `input.go`/`render.go`/`right_button.go`/`webviews.ts` lands as a tested pure
  function plus thin glue — never as more untested orchestration.

---

## Phase 3 — Build the missing test homes (seams & invariants)

The invariants the guiding rule is *about* (I7–I11) have no test today. Give them
one. This is what makes fixes *stay* fixed.

- **Preview round-trip e2e.** Extend the e2e `testhook`/`thPanes` to expose a
  preview signature, then assert: descend → reframe → ascend ⇒ the well preview
  and a sibling pane are byte-identical to before. (Today the harness cannot even
  observe a preview — that is the gap.)
- **Menu persistence & focus stability specs.** From §1a/§1b, but as full-app
  e2e: menu closes on focus change; an unfocused pane's content/preview never
  changes when another pane is acted on.
- **Cross-seam contract test.** client framing-write → `SetTile` → fanout → a
  second pane's preview updates: one test that crosses the whole seam, not a unit
  test on each side.
- **Error-surfacing pass.** Audit the swallow-to-console sites; route failures to
  a visible, assertable surface so "it disappeared" becomes "it errored, here's
  why." Add a test that a rejected optimistic edit visibly reconciles.

---

## Phase 4 — Pay down the rest, then resume features

Only once the seams above have owners and tests:

- `ListPlugins` cache/timeout so a slow plugin can't stall the launcher
  (`ARCHITECTURE.md §3`).
- localStorage in the session blob (currently cookies only), if needed.
- Resume the federation productionization (transparent multi-hop, SOCKS) — but
  not before the client/native split is stable, or it will inherit the disease.

---

## How to measure success

- **Leading indicator:** the churn god-files (`input.go`, `render.go`,
  `right_button.go`) stop appearing in fix-commits. A seam with an owner and a
  crossing test stops generating regressions.
- **Lagging indicator:** a bug reported in one session does not recur in a later
  one (the "doesn't stay fixed" set in the analysis: text disappears,
  preview green-vs-red, live-tile blank-after-descend, affordance-on-wrong-pane,
  embed-reverts-to-link-text).
- **Process indicator:** every fix commit names a root cause and answers "why was
  this not caught," and every native-layer change ships with a spec.

## Recommended order of execution

1. **Phase 0 cleanup commit** (rename `cow.go`, fix stale comments) — trivial,
   removes active misdirection.
2. **Phase 1a (menu)** — smallest change with the highest visible payoff; proves
   the one-owner pattern end to end, test included.
3. **Phase 2 extraction for the area 1b touches**, then **Phase 1b (framing)** —
   the hardest, highest-value seam, done on testable ground.
4. **Phase 1c**, then **Phase 3 test homes**, then **Phase 4**.
