---
name: ratchet
description: Turn the ledger. Take test/boundary/ratchet.txt one row at a time, each in its own worktree by an agent, lowered only by the row's mechanism, landed green on main; when no row is above its floor, run /assessment for the next one. Stops only on an empty ledger or a decision Joe has not made.
---

# Ratchet

The ledger (`test/boundary/ratchet.txt`, CLAUDE.md How to work 10) is the
scoreboard, and this skill is the loop that moves it. Read CLAUDE.md in
full first, then the ledger and `test/boundary/debt.txt`. The session that
runs this supervises; the building is done by agents in worktrees, one row
each, so the session stays responsive and judges by the ledger diff.

## The loop

1. **Pick a row.** `git status` is clean and `make check` is green at HEAD.
   Check for work already in flight: `git worktree list` and
   `git branch --list`; a branch named for a row is that row's, so review
   and land it (step 4) rather than start it again. Otherwise take the row
   with the largest distance to its floor. A row at its floor is deleted,
   its count kept as a permanent test, in a commit of its own.

2. **Brief one agent.** Opus, in a worktree off main
   (`git -C /home/joe/gridwell worktree add .worktrees/<row> -b <row> main`).
   The worktree has no `../gridwell-plugins`, so `make wasm` then
   `GRIDWELL_PLUGIN_DIR=/home/joe/gridwell go test ./...`. The brief names
   the row, its count function in `test/boundary/ratchet_counts_test.go`,
   the mechanism and decision columns verbatim, and these rules:
   - the count falls only by the mechanism the row names; the build refuses
     any other way and so does the reviewer;
   - a behaviour change is a test first, across the seam, that fails for
     the real reason (CLAUDE.md How to work 3 and 4);
   - one logical change per commit, the new recorded value in the same
     commit that lowers the count, comments true in every file touched;
   - a rise in `lines` is entered in `debt.txt` in that commit, against a
     decision Joe has already made, never one the agent invents;
   - no edit to CLAUDE.md; anything CLAUDE.md does not decide is listed in
     the report for Joe;
   - commit messages end with the attribution lines the session carries.
   The agent reports commit hashes, the row's new value, test names and
   the test output's verdict line, and leaves the worktree in place.

3. **Review by the ledger.** The diff of `ratchet.txt` and `debt.txt` is
   the first read: the row fell, no other row rose, debt entered only
   against a named decision. Then one grep for the mechanism, to see the
   count fell the way the row says. Then the tests the report names.

4. **Land it.** Cherry-pick onto main with `git -C /home/joe/gridwell`,
   never from inside a worktree. Run `make check`; run `check-electron`,
   `check-e2e`, `check-web` or `check-connections` for whatever the diff
   touches (the gate table in CLAUDE.md). Read the verdict line, not the
   tail: a red commit was pushed once by misreading a log. Push green.
   Remove the worktree and branch. Dismiss the agent.

5. **Pay the debt.** While the `debt` row is above zero, the next row is
   paying it back by deriving, not a new row (CLAUDE.md How to work 10).

6. **Find the next row.** When no row is above its floor, run
   `/assessment`. It adds rows or notes; the loop continues with the rows.

## Stopping

Stop only when the ledger holds nothing but the ceilings, or when a row
cannot fall without a decision Joe has not made. Then say the decision in
one sentence and stop; do not write it into CLAUDE.md, and do not route
around it with a count lowered another way. Clean up before stopping:
no worktree, no branch, no agent left behind.
