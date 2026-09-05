---
name: holistic-assessment
description: Read the entire production codebase and assess it holistically against Gridwell's guiding principles, lens by lens, with a CLEAN / NOT CLEAN verdict per lens and named evidence.
---

# Holistic assessment

This skill audits the whole of Gridwell against what it is for: a stable,
consistent, minimal personal space where things stay as the user left them,
plugins and remote nodes feel native, the data is long-term stable, and the
code stays maintainable by one person plus an AI. The output is a verdict per
lens with evidence, not a vibe.

**Cost warning.** This reads the entire production tree into context —
roughly 600k tokens. It needs a 1M-context model with most of the window
free. Do not run it casually; the user initiates it deliberately.

## Procedure

1. **Enumerate** the production sources. Tests, generated code, e2e,
   harnesses, and test infrastructure are excluded:

   ```
   find . -path ./.git -prune -o -type f \( -name '*.go' -o -name '*.ts' \) -print \
     | grep -vE 'node_modules|/gen/|/dist/|/out/|_test\.go|\.test\.ts|\.spec\.ts|\.d\.ts|/e2e/|e2e-web|playwright|/harness/|wire_gen|plugintest|servertest|dialtest|shellsvctest|test/boundary' \
     | sort
   ```

2. **Read all of it.** Every file, in full, with the Read tool — no skimming,
   no sampling, no delegating the reading to subagents (the whole point is one
   context holding the whole system). Also read `CLAUDE.md`,
   `ARCHITECTURE.md`, `internal/local/store/CLAUDE.md`,
   `docs/debt-program.md`, and `docs/flake-ledger.md`.

3. **Assess through the lenses below.** For each: CLEAN or NOT CLEAN, with
   the evidence named (`file:line` or package). A lens is NOT CLEAN on one
   real counterexample; suspicion without evidence is a note, not a verdict.

4. **Report**: the per-lens table first, then the narrative (verdict /
   strong / strains / net), then — if any lens is NOT CLEAN — what would
   make it clean, concretely. Compare against the previous assessment
   (baseline: 2026-09-05, recorded in `docs/debt-program.md`) and say what
   moved.

## The lenses and their bars

**1. Data stability & format contract.** The store honors
`internal/local/store/CLAUDE.md`: frozen v1 untouched, every schema change
via descriptor + migration + fixture, sequences preserved, ids never reused,
no DB deleted to absorb a change, blobs self-describing. Config writes limited
to minting. CLEAN when no violation of the written contract is found.

**2. One fact, one owner.** No fact stored or derived in two places unless
one derives from the other; every gesture's preview and commit share one
verdict function; forced copies at the native seam are pinned by drift tests.
CLEAN when no unpinned duplicate fact is found.

**3. Decisions live in pure packages.** A policy decision — any branching on
world state that changes a user-visible outcome — lives in a js-free
`client/*` package or a pure Electron module, with unit tests. The wasm shim
and Electron main resolve impure inputs and execute effects, nothing more.
Orchestration counts as policy: navigation, restore, go-live, and takeover
sequencing must be pure state machines, not interleaved awaits with
moved-on guards hand-written per path. CLEAN when no multi-arm policy
decision exists only in `client/wasm/*` or `apps/desktop/src/main/webviews.ts`
without a pure, tested twin.

**4. Concept economy & exception surface.** Enumerate the user-facing
concepts and states (tile kinds, link shapes, ephemeral/trash/levels,
dead/dark/stale/broken/rootless, framing vs. content vs. capture). Each
distinction must earn its place, and the plugin exception surface
(read-only, host_content, serves_page, text_presentation, stale, dead) must
route through named single-owner predicates rather than per-call-site arms.
CLEAN when no two concepts could merge without losing a decided behavior AND
no exception check bypasses its owning predicate.

**5. Native seam robustness.** No timing-heuristic correctness mechanism
(grace windows, settle timers, recheck delays) where an owned fact could
exist instead. Any that remain are documented as unavoidable, with the
rationale, and covered by a harness or e2e spec. Every `webviews.ts` code
path has a test. CLEAN when the remaining heuristics are all
documented-unavoidable and tested.

**6. Freshness stack comprehensibility.** The layered consistency flow —
source → pluginhost overlay → sourcecache → client cache → outbox/retry — is
documented in one place (`docs/freshness.md`), each layer's rule stated, and
the doc matches the code. Cross-layer behavior (stale serve → revalidate →
GridChanged → client refetch; dark transitions; the echo interlock) is
covered by seam tests. CLEAN when the doc exists, matches, and the seams are
tested.

**7. Errors surface.** Every failure path reaches `client/errsurface`,
`sendError`, or a health event — grep for swallowed errors (log-and-return,
discarded `err`) on user-visible paths. Local state drops only on a server
verdict. CLEAN when no swallow is found.

**8. Test posture.** Every pure package has real tests; the native gates
cover what `make check` cannot; `docs/flake-ledger.md` has no unexplained
OPEN rows; every bug-shaped comment ("this happened once") has a pinning
test. CLEAN when no untested pure package and no unexplained flake remains.

**9. Maintainability.** Comments record decisions and constraints, not
mechanics; `client/wasm` App state is grouped by owner; no policy-bearing
file has grown past ~1.5k lines without a recorded reason; `docs/` matches
the code it describes. CLEAN when a fresh reader could reconstruct the
invariants from the tree alone — name anything that would mislead them.

## Notes

- The bar for CLEAN is a *found counterexample*, not perfection-by-assertion:
  say what you looked for and did not find.
- Scale/performance ceilings (full repaint, O(n) cache walks) are decided
  trade-offs, not debt — note them only if the decision comment is missing.
- Record nothing in the repo from this skill itself; the report goes to the
  user, who decides what becomes a workstream in `docs/debt-program.md`.
