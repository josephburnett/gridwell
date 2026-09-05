# The debt program

The 2026-09-05 holistic assessment (the whole production tree read in one
context; procedure now in `.claude/skills/holistic-assessment`) judged the
architecture strong — the data layer, the one-owner discipline, and the
namespace abstraction came up clean — and found the debt concentrated in
four places. This program's goal is that the next assessment comes up CLEAN
on every lens. Each workstream below names the deficiency with evidence, the
end state that satisfies the lens's bar, and the approach. Design docs live
in `docs/debt/`; status is tracked here.

## Baseline (2026-09-05)

| Lens | Verdict |
|---|---|
| 1. Data stability & format contract | CLEAN |
| 2. One fact, one owner | CLEAN |
| 3. Decisions in pure packages | **NOT CLEAN** — W1, W6 |
| 4. Concept economy & exception surface | **NOT CLEAN** — W4 |
| 5. Native seam robustness | **NOT CLEAN** — W2 |
| 6. Freshness stack comprehensibility | **NOT CLEAN** — W3 |
| 7. Errors surface | CLEAN |
| 8. Test posture | CLEAN (contingent on W1/W2 not regressing it; one explained OPEN flake-ledger row) |
| 9. Maintainability | CLEAN (contingent on W6) |

## W1 — Navigation orchestration becomes a pure state machine

**Deficiency (lens 3).** The shim's largest remaining policy mass is the
navigation/restore/go-live orchestration: `descend`/`descendGrid`/
`descendContent`/`ascend`/`ascendOnce`/`leaveFrame`/`landOnFrame`
(`client/wasm/nav.go`), `autoLiveOnDescent`/`autoLiveOnRestore`/
`healStalePanePath` (`client/wasm/input.go`), `applyURLState`/
`restoreFromHistory` (`client/wasm/urlsync.go`), `descendLevel`/
`installWorkspace`/`ascendLevels`/`bootWorkspace` (`client/wasm/workspace.go`),
and `finishPromote` (`client/wasm/input.go`). These interleave awaits with
hand-written moved-on guards (`pane.StillDescended` checks re-spelled per
path) and sequence writebacks, stream teardown, frame pushes, transitions,
and go-live per call site. The recent 15-bug sweep found its bugs almost
entirely here. `make check` executes none of it.

**End state.** A js-free package (working name `client/nav`) owns the
orchestration as data: given a gesture (descend into tile T, ascend N,
restore place P, promote, enter/leave level) and a world snapshot, it
returns an ordered effect list — flush framing, close stream (freeze y/n),
push/pop frame, start transition with segments, fetch grid, decide go-live,
re-engage — plus, for the async paths, an explicit continuation keyed by a
moved-on token, so the guard is the machine's state rather than a re-spelled
check. The shim executes effects and feeds async results back in. Every
path, including the races (pane closed mid-fetch, user moved on mid-probe,
displaced transition), is table-tested. `nav.go`, the orchestration halves of
`urlsync.go`/`workspace.go`, and the restore paths shrink to effect
executors.

**Approach.** Incremental, one verb family per phase, each phase green on
`make check` + `make check-e2e` before the next:
1. Phase A: descend/ascend (nav.go) — the effect vocabulary is established
   here.
2. Phase B: the restore paths (applyURLState, restoreFromHistory,
   autoLiveOnRestore, healStalePanePath).
3. Phase C: workspace levels (descendLevel/installWorkspace/ascendLevels/
   bootWorkspace) and promote — plus the go-live link hop, which held the last
   hand-written moved-on check.
Design doc: `docs/debt/w1-nav-orchestration.md`.

**Status:** [x] design [x] phase A [x] phase B [x] phase C — DONE. `client/nav`
owns descend, ascend, restore, the pane-tile levels and the promote; the shim
gathers, executes, and feeds answers back. `grep -rn StillDescended
client/wasm` is empty: every moved-on check is one Guard, evaluated in one
place. Open: W6's App-struct grouping, which rode this and is now the only
piece of W1's end state left.

## W2 — The native seam sheds its timing heuristics

**Deficiency (lens 5).** The focus-steal guard in
`apps/desktop/src/main/webviews.ts` is timing-heuristic correctness:
`USER_CLICK_FOCUS_GRACE_MS = 1500` bridges "native focus lands now, wasm
marks focused a round trip later", and `FOCUS_RECHECK_MS = 120` re-checks
after an in-flight widget-focus commit. It is the one mechanism in the
system that works by racing the clock instead of owning a fact, and the
decision itself (focused? recent click? bounce?) is inline in `wireNav`
rather than a pure tested function.

**End state.** Either the heuristic is replaced by an owned fact (candidates:
the preload's forwarded left-down already stamps intent — investigate making
the renderer's focus ack explicit so the grace window closes on the ack
rather than on a clock; or track Electron's own `webContents` input events),
or it is proven irreducible and then: the bounce decision extracted into a
pure module (`apps/desktop/src/main/focusguard.ts`) with unit tests over
(focused, lastClickMs, now, recheck) tables, the constants documented as
unavoidable with the rationale in place, and the steal/bounce/recheck paths
covered in the bridge harness. Separately: audit every `webviews.ts` path
against the CLAUDE.md rule ("anything you touch there gets one") and close
gaps. Design doc: `docs/debt/w2-native-seam.md`.

**Status:** [x] design [x] focusguard extraction + tests [x] harness coverage [ ] webviews path audit (10 GAP rows left in docs/debt/w2-native-seam.md §6; the focus family is closed)

## W3 — The freshness stack gets its one document and its seam tests

**Deficiency (lens 6).** Five layers each own a freshness rule — the plugin
adapter's overlay/stale stamp (`internal/pluginhost/adapter.go`), the
sourcecache's serve-first window + dark tracking + revalidation
(`internal/sourcecache`), the client cache's echo interlock and content
basis (`client/cache`), the inflight claims (`client/inflight`), and the
outbox/retry (`client/outbox`, `retryKick`) — and no single document traces
a fact through them. Future "why is this stale" debugging pays the full
archaeology cost each time.

**End state.** `docs/freshness.md`: the layers named in order, each layer's
rule in its own words, and three end-to-end traces — (a) stale serve →
revalidate → GridChanged → client refetch, (b) a connection going dark and
coming back (health both directions, the cached chip), (c) a write racing
its own echo (the interlock, the outbox drain). Plus a gap list: any
cross-layer behavior in those traces not pinned by a seam test gets one
(most live in `internal/server`'s seam tests and `make check-connections`).
Simplification candidates noted for owner decision, not silently applied.

**Status:** [x] doc [x] seam-test gap list [ ] gap tests

## W4 — Concept audit and exception-surface consolidation

**Deficiency (lens 4).** Two halves. (a) The user-facing concept count has
drifted up (tile kinds + exit wells + leaf links + ephemeral visits + trash
+ workspace levels; dead/dark/stale/broken/rootless; framing/content/
capture) — each defensible alone, unexamined as a sum. (b) The plugin
exception surface leaks: `ServesPage` is checked at ~10 wasm call sites,
`tileReadOnly`/`TextPresentation`/`HostContent` arms are sprinkled where a
named predicate should own the question (some owners exist — `WebContent()`,
`tileReadOnly` — but not all checks route through one).

**End state.** (a) `docs/concepts.md`: the full enumeration, what each
distinction buys, and explicit merge candidates put to the owner as
questions — this half ends in owner decisions, not code. (b) Every
exception check in `client/wasm` routes through a named single-owner
predicate (in `api/rpc` on Tile, or a `client/*` package), with the sweep
verified by grep: no bare `.ServesPage`/`.TextPresentation`/`Meta.HostContent`
read outside its owner. Design doc: `docs/debt/w4-concepts.md`.

**Status:** [x] design (docs/debt/w4-concepts.md: enumeration + 8 merge questions for the owner) [ ] merge decisions [x] predicate sweep (19 of 20 sites; six new owners; `scripts/check-exception-owners.sh` keeps it swept. One site left as a question — `content_zoom.go`'s serves_page arm, where the owning predicate would change behavior)

## W6 — The App struct decomposes by owner

**Deficiency (lenses 3, 9).** `client/wasm/main.go`'s `App` holds ~40 fields
of interlocking session state mutated from many files. Ownership is
documented per field but not enforced by shape; a new feature can reach any
field from anywhere.

**End state.** Fields grouped into owned sub-structs matching the files that
own them (fetch machinery: the three inflight sets + failure latches;
overlay/DOM singletons; persistence schedulers + counters; navigation
session state — which W1 largely absorbs), each constructed in one place,
so cross-owner mutation is visible at the call site as `a.fetch.…` vs
`a.overlays.…`. No behavior change; rides W1's phases rather than preceding
them.

**Status:** [ ] design (folded into w1 doc) [ ] extraction

## Order and rules

W3 first (cheap, and its doc de-risks everything else), W2 second
(contained), then W1 phase by phase with W6 riding it, W4's code half
anywhere, W4's concept questions whenever the owner has time.

Every phase obeys CLAUDE.md: one logical change per commit, a failing test
first for anything bug-shaped, the native gates when the native layer is
touched, and no behavior change without a decision. Workers run SERIALLY in
this checkout when any gate runs (the gates rebuild at start). When a phase
completes, tick it here in the same commit.
