---
name: assessment
description: Find the next class the ledger does not count. Expectations written first without the code, a trace from a node at HEAD, targeted reads at the owners, and a fact index per tree. The output is ledger rows and notes, never verdicts.
---

# Assessment

The ledger (`test/boundary/ratchet.txt`, CLAUDE.md How to work 10) is what
converges the code: every known class of violation is a count the build
runs, and a unit of work lowers one row. This skill has one job, the one
the ledger cannot do for itself: find a class it does not yet count, and
add it as a row. A finding with a count is a row. A finding with no count
is a note in the report, and a note that recurs is a sign the class needs
a count. Nothing here re-judges what a count already holds.

Two kinds of defect exist and they are found differently. A defect of
shape (a fact spelled twice, a decision in the shim) is found by reading
at the owner. A defect of absence (an arm never built, a walk run for
nobody) cannot be read, because there is nothing to read; it is found by
holding the code to a bar written before the code was opened, and by
watching the product run. Steps 2 and 3 find absence; steps 4 and 5 find
shape. The whole tree is never read into one context: the counts hold the
tree, and reading it again confirms them at the cost of a window.

## Procedure

1. **Refuse unless the node is at HEAD.** The tree is clean (`git status`),
   and the node the trace will come from runs that commit: a dump's file
   name carries its build (`trace-<time>-<commit>.jsonl`, see
   `tracewire.BuildCommit`) and must equal `git rev-parse --short HEAD`
   with no `+dirty`. Otherwise stop and say so: a trace from another build
   is a false reading. Tell the user to rebuild and restart the serve; never
   restart it yourself (2026-10-22). `make check` is green at HEAD, so every
   counted class is at its recorded value before the hunt starts.

2. **Write the expectations without the code.** Spawn one agent that is
   given ONLY `CLAUDE.md`, `api/gridwell/v1/data.proto`,
   `api/plugin/v1/plugin.proto`, `docs/concepts.md` and `docs/freshness.md`,
   and is forbidden to open any other file. It writes `expectations.md`
   into the scratchpad: for every promise, the concrete behaviours that
   must hold; the parity cells, kind times operation times owner, each
   "same" or "decided: <CLAUDE.md line>"; for every wire verb and event,
   who produces and consumes it; every duration the design declares. A cell
   or row it cannot fill from those sources is "unspecified", and the
   unspecified list is an input to step 4. The checker never edits it.

3. **Read the trace.** Ask for a fresh dump (the Dump logs row) of a few
   minutes of real use. Tabulate it: every rpc by verb and outcome, every
   error code, every repeated ask of one id, every id of the wrong shape,
   every event followed by a refetch, every write the user did not make,
   every walk or capture nothing on screen needed, every plugin log line.
   Each anomaly is a finding until a decision in CLAUDE.md explains it. The
   trace found the largest defect of runs 11 and 12, and the 2026-09-27
   trace found four in an hour.

4. **Read at the owners.** Three lists name what to read, and each item is
   read at its owner with its callers (grep), never the tree:
   - every trace anomaly from step 3;
   - every unspecified item from step 2, read to learn what the code
     decided without a decision;
   - the diff since the last assessment (the last commit whose message
     begins `boundary: the ledger counts`), read whole in one context,
     because a second spelling of a fact can only have entered there.
   A comment that explains why something is NOT done is a claim, not a
   decision; it is a decision only if CLAUDE.md says so, and otherwise it
   names a hole.

5. **Index the facts.** One agent per top-level tree (`api`, `internal`,
   `client`, `apps/desktop`), given CLAUDE.md and that tree alone, writes
   into the scratchpad: the facts the tree owns (stored or derived there),
   by name and file; the facts it reads from elsewhere, by the name it uses
   and the owner it believes holds them; and every comment that says why
   something is not done. Cross-check the four lists in one context: one
   fact under two names, a reader naming an owner that does not own it, a
   harness that re-spells a shape the node runs (the 2026-09-06 connection
   API outage was one), and a claim comment with no decision. This is the
   one class no count finds yet, and it is the only reason an agent reads
   a package end to end.

6. **Hunt by principle.** For each principle in CLAUDE.md, the count that
   already holds it and where a new instance hides. Look only where the
   count does not.
   - *Things stay as you left them.* Held by the store contract, the claim
     refusals, the migration fixtures. Hides in: a new write verb or arm
     since the last run; a write in the trace the user did not make; a
     read that mutates.
   - *One fact, one owner.* Held by the copied-constant and doc-name
     counts, the boundary tests on the API shapes, and the open rows. Hides
     in: step 5's cross-check; a cache with a second freshness.
   - *Every case is a cell.* Held by the parity table and the flag guard.
     Hides in: a new wire field or flag with no axis; an unspecified cell
     the code decided alone, which is a decision missing from CLAUDE.md.
   - *Nothing silent.* Held by the timer list, the log-only arm list, the
     trace budget, the verb count. Hides in: the trace (asks repeated, a
     walk for nobody, an event answered with a refetch); a failure that
     reaches no notice; a clock asking a source that can tell.
   - *Small enough to hold whole, and only shrinking.* Held by the
     ceilings and the open rows. Hides in: a file past ~1.5k lines with no
     recorded reason; state kept outside its owner; a doc that no longer
     matches the code it describes.
   - *Decisions in pure code, everything else glue.* Held in part by the
     boundary tests. Hides in: the diff of `client/wasm` and
     `apps/desktop/src/main` since the last run, a multi-arm branch with
     no pure, tested twin; a timing heuristic where an owned fact could be.
   Inherited behaviour is a seam too: every `deps:` commit since the last
   run names the gates it ran, and a decision comment that relies on a
   platform behaviour has a test that would fail if the platform changed it.

7. **Add the rows.** Each class found with a count becomes one row, in its
   own commit, built by an agent in a worktree: the count in
   `test/boundary/ratchet_counts_test.go`, registered in `ratchetCounts`,
   the row in `ratchet.txt` with its recorded value, floor, owner, the one
   mechanism by which it may fall, and the decision it serves. The row is
   the finding's home; the chat is not. The commit message says what the
   class is and why it is a hole.

8. **Report**, short and plain: the rows added, each with its count and
   mechanism; the notes, each with why it has no count yet; the
   expectations disagreements, both readings, for the user to decide; the
   trace anomalies and the decision that explains each. No lens table, no
   verdict per principle, no matrix: the build holds those.

## Notes

- The expectations agent must not read the code, and the checker must not
  edit `expectations.md`. When they disagree, report both readings; the
  user decides, and the answer becomes a promise, a decision, or a fix.
- A finding is a counterexample with a file and line, never a suspicion.
  Say what was looked for and not found.
- Scale ceilings (a full repaint, an O(n) cache walk) are decided
  trade-offs, not findings, unless the decision comment is missing.
- A row the user decides against is a decision in CLAUDE.md; the row's
  count then becomes a permanent test of the decided value.
