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
| `apps/desktop/e2e/workspace-clone.spec.ts` | a one-run flake of an earlier version of the spec | descending inside the settle window trusted a cached row with a stale `BlobID 0`, so the WRITABLE default could be installed and the persister overwrite the blob | the app refetches the tile in `startWorkspaceDescent`; the spec pins the trap |
| `apps/desktop/e2e/fixtures.ts` | (the fixture side of the two entries above) | teardown hang after failure; boot not done at hook-install | teardown that completes from any end state, with leak checks; readiness means anchored |

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
