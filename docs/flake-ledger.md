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
| `apps/desktop/e2e/shell-link-open.spec.ts` (the OSC 8 test) | 2026-09-05, one failure at test 119 of a full 168-spec run on a freshly built tree: `markerRow` stayed `-1` for the full 10 s while the terminal was alive and focused, and the same tree passed four times in isolation | the test wrote its OSC 8 row with `shellFeed` as soon as the RENDERER attached, which is before the `/shell` socket is even dialled; tmux clears the screen when it paints the attach, so a row written ahead of that paint is erased and never returns. The two neighbours in the same file never flaked because their round trip through the PTY is itself proof of the attach, and `shell-bare-lf.spec.ts` hid the same race behind a re-feed on every poll attempt | one owner for "a write can land now": `GridwellDriver.shellAttached()` waits for a command's own output row, and both feeding specs wait on it. Reproduced deterministically by feeding from inside the page the instant the conn appears — the row is gone within 200 ms, and behind the wait it stays |
| `apps/desktop/e2e/teardown-dirty.spec.ts` | an unattributed teardown error that read as a load flake | after a failing test, `electronApp.close()` never settled; the worker was SIGKILLed at the timeout, leaking tmux servers and the home | the fixture's teardown completes from ANY spec end state and runs the leak checks (sidecar assert, tmux kill); this spec ends deliberately dirty to keep that pinned |
| `apps/desktop/e2e/errsurface.spec.ts` | the pre-2026-08-07 "inverse" flake: isolated runs saw zero `SetTile` posts | a synthetic wheel can be DROPPED under xvfb, leaving the settle persister nothing to persist, and the spec timed out on the far-end notice | the pane's own framing is the delivery ack; the spec resends an undelivered wheel |
| `apps/desktop/e2e/url-history.spec.ts` | the restored-view back-navigation under suite load | a fixed post-`goBack` sleep — the navigation can take longer than any constant | poll the landing (`expect.poll`) |
| `apps/desktop/e2e/workspace-clone.spec.ts` | a one-run flake of an earlier version of the spec | descending inside the settle window trusted a cached row with a stale `BlobID 0`, so the WRITABLE default could be installed and the persister overwrite the blob | the level fetch refetches the tile rather than trusting the cached row (`client/nav/level.go`); the spec pins the trap |
| `apps/desktop/e2e/fixtures.ts` | (the fixture side of the two entries above) | teardown hang after failure; boot not done at hook-install | teardown that completes from any end state, with leak checks; readiness means anchored |

## Open: no mechanism yet

| Spec | What flaked | What the evidence says | State |
|---|---|---|---|
| `apps/desktop/e2e/lost-release.spec.ts` | 2026-09-04, three failures in six minutes (19:01:34, 19:05:14, 19:07:02 local) in combined runs; never in isolation | All three are the same assertion and the same value: `ghost().hiddenTileID` was `""` at the FIRST poll, ~2 ms after the arming turn returned, and stayed `""` for the full 5 s. Three states read that way: nothing armed, a pan armed on a cell holding no tile, and a tile drag armed and finished before the read. The 2026-09-04 dump could not tell them apart. **Ruled out by construction (2026-09-14):** an overlay eating the press — the spec calls `canvas.dispatchEvent`, so the canvas listener runs whatever is painted over it, and the window-level move and up gate on `target == canvas`, which is what was dispatched; a transition (`a.trans.Any()`) — `startTransition` has one caller, `nav_exec.go`, and the spec's last navigation ended in `waitIdle`, which gates on `!trans.Any()`; the palette — `commitTemplateDrop` ends with `menu.Close()` on the drop itself, before `dragCreate`'s `waitIdle` returns; the divider grab — `armLeftResize` arms only within the 10px band of a pane edge, and the press is at the pane's centre, over 350px from every edge, so no rect a work-in-progress grab could be handed reaches it; and a layout that moved under a precomputed point (a notice strip raised between the read and the press moves every pane rect by half `errsurface.StripHeight`) — the turn that presses now resolves its own cells. **Ruled out by measurement (2026-09-14, freshly built tree, 4 spinners on 4 cores throughout):** Chromium's own buttons-0 move, which the pre-`a193b45d` two-turn shape did leave a window for — 20 reps of that exact shape with every `isTrusted` mousemove recorded saw not one arrive; and a client cache lagging the server — 150 creates comparing each pane's `tileIds` against the `GetGrid` oracle saw no divergence. **Still standing:** a press misrouted by a client the run did not build. The failures sit inside the half hour a peer session on the same box was editing and rebuilding exactly that press path (`d04f7323`, committed 19:07:18), and every test gets a fresh app, home and profile, so the built binaries and the machine are the whole coupling. | NOT closed; no mechanism. Unreproduced in 41 consecutive runs on a freshly built tree: the 21 of 2026-09-04 (the full suite twice, `--repeat-each` under load, three neighbour combinations) plus 20 loaded `--repeat-each` reps on 2026-09-14. One family was found and removed rather than measured away: `inflight.Set.Begin` dropped a refetch asked for while one was in flight, so a `GridChanged` racing its own grid's fetch left the client without the row the user just made — a press over it would arm a pan and print exactly this signature (`b3009b13`). The spec now resolves its press cells inside the arming turn and the dump carries the facts the occurrence could not answer: the resolved points, the strip height, and each pane's client tile ids; and it asserts, before pressing, that the client holds the tile the server holds. Next occurrence names its family. |
| `apps/desktop/e2e/fixtures.ts` (the `gw` fixture) | 2026-09-14, twice in 40 reps run under sustained CPU starvation (4 spinners on 4 cores) | `homePassword` threw `ENOENT .../web-password` after the `window` fixture had already resolved, so a renderer was up and anchored while the home it was launched on held no minted password. `EnsurePasswordFile` writes the file at node construction, before anything serves, so the home that served the window was not the home the fixture read. Not seen unloaded. | NOT closed. Two occurrences, no mechanism. Next occurrence should record the origin `window.url()` reports and what the home directory actually contains. |

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
