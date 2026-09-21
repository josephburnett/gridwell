# The flake ledger

Every e2e spec whose history includes a flake, what the flake was, and how
it was closed. The notes live as comments in the specs (grep `flak` under
`apps/desktop/e2e`); this page indexes them. `test/boundary` pins that every
spec carrying such a note is listed here, and the gates read it from the other
side: a retry that passes a spec with no row here fails the job
(`scripts/flaky-report.mjs`).

Two rules:

- A flake is a bug with a diagnosis pending. Every entry below ended as a
  mechanism, never as "it was just flaky". A retry that passes is evidence,
  not a verdict.
- A rerun only vindicates a spec on a freshly built tree. The gates rebuild
  at start; a single-spec `npx playwright test` does not. Run `make build`
  and `npm run build` first. A run tests the artifacts it copied at setup
  (`apps/desktop/e2e/runtree.ts`), so it is self-consistent — that is not the
  same as fresh.

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
| `apps/desktop/e2e/fixtures.ts` (the `gw` fixture) | 2026-09-14, twice in 40 reps run under sustained CPU starvation: `homePassword` threw `ENOENT .../web-password` after the `window` fixture had already resolved, so a renderer was up and anchored on a home that held no minted password | a second `npx playwright test` on the same box ran its global setup while the first run's test was mid-flight, and the sweep took EVERY `gridwell-e2e-*` directory in the shared `os.tmpdir()`, live ones included. The sidecar holds its config, its password and its SQLite descriptor in memory, so the app kept serving and the window resolved; the next read of the home from the outside is `homePassword`, and it found nothing. The load that day was nine agents running Electron specs at once | a home's name carries the pid of the process that made it (`makeHome`) and the sweep skips a home whose owner is alive, keeping the guarantee it exists for: a SIGKILLed run's pid is gone, so its homes still go (`apps/desktop/e2e/homes.ts`, `apps/desktop/e2e/homes.test.ts`). Reproduced deterministically by running the real sweep from a second process the moment the live run's node minted its password — the spec fails at `fixtures.ts:101` with exactly this ENOENT, and passes behind the fix |
| `apps/desktop/e2e/lost-release.spec.ts` | 2026-09-04, three failures in six minutes (19:01:34, 19:05:14, 19:07:02 local) in combined runs, never in isolation, and never again in 41 later runs: `ghost().hiddenTileID` was `""` at the first poll and stayed `""` for the full 5 s, with the client's `tileIds` holding the tile | the one coupling the evidence left standing, and a real one: the harness had no fact "the tree this run tests". Every test resolved the sidecar, the plugin binaries and `web/` from the shared checkout at its own launch, so a peer session rebuilding that checkout mid-run — `d04f7323` rewrote the press path and landed at 19:07:18, and `go build` wrote `web/gridwell.wasm` in place until `d4839255` — handed the tests that had not started yet a client no gate built. The three failures sit inside that half hour. Everything else the occurrence could have been was ruled out first: an overlay eating the press, a transition, the palette, the divider grab, a layout that moved under a precomputed point, Chromium's own buttons-0 move, a client cache lagging the server, and a concurrent run's leak sweep taking the home | the run copies the artifacts once, at global setup, and every launch reads that copy (`apps/desktop/e2e/runtree.ts`, `global-setup.ts`, `global-teardown.ts`); `test/boundary` pins that no other e2e file names a launch artifact, and a dead run's snapshot goes to the same sweep as its homes. Demonstrated both ways on a freshly built tree: swapping in a wasm whose press path does not arm a tile drag, after the run had started, failed every later rep with exactly this signature — `hiddenTileID` `""`, `tileIds` holding the tile — and behind the pin the same swap, plus deleting `gridwell-plugin-fs` from the checkout mid-run, left all 18 tests green. The 2026-09-04 intermediate build is not recoverable, so this closes the class, not that build |

## Open: no mechanism yet

None. Every entry above ended as a mechanism.

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
