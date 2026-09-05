# The flake ledger

Every e2e spec whose history includes a flake, what the flake was, and how
it was closed. The notes live as comments in the specs (grep `flak` under
`apps/desktop/e2e`); this page indexes them. `test/boundary` pins that every
spec carrying such a note is listed here.

Two rules:

- A flake is a bug with a diagnosis pending. Every entry below ended as a
  mechanism, never as "it was just flaky". A retry that passes is evidence,
  not a verdict.
- A rerun only vindicates a spec on a freshly built tree. The gates rebuild
  at start; a single-spec `npx playwright test` does not. Run `make build`
  and `npm run build` first.

## Specs with a flake history (all closed)

| Spec | What flaked | Mechanism | Closed by |
|---|---|---|---|
| `apps/desktop/e2e/pane-view-gestures.spec.ts` | the #195 "stack hygiene" round trip, a long history | a computed cell center could land OFF-PANE at the child grid's zoom, so the ascend click was a silent no-op; and a first `focused()` on a slow boot read `anchor=""` (the load-sensitive half) | pane-center middle-click ascends (position-independent); the fixture waits for the pane to be anchored before handing over the window (`apps/desktop/e2e/fixtures.ts`) |
| `apps/desktop/e2e/shell-link-open.spec.ts` | the 2026-08-06 "load flake" | the spec raced itself: a whole-buffer `toContain` was satisfied by the TYPED command line (which carries the marker), so on a slow echo the selection indexed the wrong row | match the OUTPUT row explicitly (`outputRow`) |
| `apps/desktop/e2e/teardown-dirty.spec.ts` | an unattributed teardown error that read as a load flake | after a failing test, `electronApp.close()` never settled; the worker was SIGKILLed at the timeout, leaking tmux servers and the home | the fixture's teardown completes from ANY spec end state and runs the leak checks (sidecar assert, tmux kill); this spec ends deliberately dirty to keep that pinned |
| `apps/desktop/e2e/errsurface.spec.ts` | the pre-2026-08-07 "inverse" flake: isolated runs saw zero `SetTile` posts | a synthetic wheel can be DROPPED under xvfb, leaving the settle persister nothing to persist, and the spec timed out on the far-end notice | the pane's own framing is the delivery ack; the spec resends an undelivered wheel |
| `apps/desktop/e2e/url-history.spec.ts` | the restored-view back-navigation under suite load | a fixed post-`goBack` sleep — the navigation can take longer than any constant | poll the landing (`expect.poll`) |
| `apps/desktop/e2e/workspace-clone.spec.ts` | a one-run flake of an earlier version of the spec | descending inside the settle window trusted a cached row with a stale `BlobID 0`, so the WRITABLE default could be installed and the persister overwrite the blob | the app refetches the tile in `descendLevel`; the spec pins the trap |
| `apps/desktop/e2e/fixtures.ts` | (the fixture side of the two entries above) | teardown hang after failure; boot not done at hook-install | teardown that completes from any end state, with leak checks; readiness means anchored |

## Open: no mechanism yet

| Spec | What flaked | What the evidence says | State |
|---|---|---|---|
| `apps/desktop/e2e/shell-link-open.spec.ts` (the OSC 8 test) | 2026-09-05, one failure at test 119 of a full 168-spec run on a freshly built tree | The fed OSC 8 sequence never reached the terminal buffer: `markerRow` was `-1` at every poll for the full 10 s, so the click was never attempted and nothing about the link path was exercised. The page snapshot shows the terminal textarea alive and focused, and the same built tree passed the test four times in isolation (once plus `--repeat-each=3`). The rendering path is upstream of everything that run's change touched (W1 phase C, navigation), so what is left is a fed sequence lost or delayed between `shellFeed` and xterm's buffer under suite load. | NOT closed. One occurrence, no mechanism. Next occurrence should capture the buffer and the renderer state at the timeout rather than a bare `-1`. |
| `apps/desktop/e2e/lost-release.spec.ts` | 2026-09-04, three failures in six minutes (19:01:34, 19:05:14, 19:07:02 local) in combined runs; never in isolation | All three are the same assertion and the same value: `ghost().hiddenTileID` was `""` at the FIRST poll, ~2 ms after the arming turn returned, and stayed `""` for the full 5 s. So the ghost never existed — the press never armed a tile drag — rather than arming and dying. That rules out the story the spec's own comment tells (a buttons-0 move Chromium emits on its own, ending the drag early): such a move would have to land inside a 2 ms window three times running, and the spec's arming already runs in one turn. It also rules out the palette (measured closed at the press) and the divider grab (one pane: no adjacent divider to grab). What is left is a press swallowed or misrouted by the built client — and the failures sit inside the half hour a peer session on the same box was editing and rebuilding exactly that press path (`d04f7323`, "A press at a pane corner grabs one divider per axis", committed 19:07:18), which is the only thing a combined run shares with its neighbours: every test gets a fresh app, home and profile, so the built binaries and the machine are the whole coupling. | NOT closed. Unreproduced on a freshly built tree in 21 consecutive runs: the full 168-spec suite twice, 10× `--repeat-each` under concurrent load, and three neighbour combinations. The spec now delivers press → threshold → lost release in ONE turn and reads its mid-gesture state inside that turn, so there is no window at all for a platform move, and a repeat prints the whole armed state (`ghost`, `idleDetail` with all three armed gesture states, palette, panes) instead of a bare `""`. Next occurrence names itself; until then this row is the open bug. |

## Environment-only failures (not spec flakes)

- **CI runner tmux socket dir** (`.github/workflows/gates.yml`): the
  image leaves a `/tmp/tmux-<uid>` tmux refuses ("unsafe permissions"),
  so every shell spec failed on the runner and nowhere else. The
  workflow removes and re-mints it 0700 before the suite and uploads
  traces on failure — the shell-spec failures were never a missing
  tmux, and never reproduced on a dev box.

## Adding an entry

When a spec gains a flake note, add its row here in the same commit:
spec path, what was observed, the mechanism in one sentence, and what
closed it. An entry without a mechanism is an open bug, not a ledger
line — say so in the "Closed by" column and keep digging.
