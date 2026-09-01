# Plugin state

## Proposal

The node hands every plugin a private directory, `<home>/plugins/<plugin-id>`,
where `<home>` is the Gridwell home (`GRIDWELL_HOME`, else `~/.gridwell` —
wherever the `server.yaml` in use lives). The plugin may keep anything it
wants there. The gitlab plugin uses it to cache its walked todo lists, so a
restart serves instantly from disk, every later walk is a delta from what it
already knows, and a background refresher keeps the memory warm on a timer.

This amends the "a plugin is stateless" decision. The half that mattered
stays: a plugin holds no node facts — no ids, no layout, no arrangement, no
access to the node's store. What changes: a plugin may keep its own memory of
*its source's* data, in the directory the node hands it, under the same
contract as `cache.db` — disposable, safe to delete, rewarms from use.

Non-requirements: no node-provided KV API (a plugin that can't touch disk is
a problem for the day it exists); no backup of state directories; no
migration of anything (the gitlab cache warms itself with one walk); no
GraphQL client.

## The mechanism (gridwell repo)

- `internal/plugin/loader.go` `loadPlugin`: create `<home>/plugins/<pc.ID>`
  (0700, MkdirAll) and pass it to the plugin as `state_dir` in the config
  map, beside the existing `uuid` and `kind` keys. The home dir comes from
  the config package's one `Home()` derivation, threaded to the loader —
  never re-derived.
- Never auto-delete: a plugin removed from `server.yaml` may return, the
  directory is small, and "things stay as you left them" wins. Deleting a
  state directory by hand is always safe.
- `docs/plugin-authoring.md` documents the key and the contract sentence
  above. `CLAUDE.md`'s decision list and `ARCHITECTURE.md`'s plugin paragraph
  drop "stateless" for the amended wording.
- `internal/plugintest` passes a per-test temp `state_dir` so no test writes
  a real home, and a test pins that the loader mints the directory and the
  key.

## The gitlab plugin (gridwell-plugins repo)

- One cache file in `state_dir` (e.g. `todos.json`), written atomically
  (temp file + rename) after every successful walk: the todo records, plus
  `doneComplete`. ~7k todos is a few MB of JSON; if that chafes, the format
  is the plugin's own to change.
- Boot: load the file into `Memory` before serving. A restart then answers
  every listing instantly, and `doneComplete` restored from disk is the
  high-water mechanism the walk already has: the done walk stops at the
  first page carrying nothing unknown, so a warm walk is the pending pages
  (the live set, which must always be walked in full for done-by-absence to
  hold) plus one or two done pages.
- Resilient walks: a failed page retries in place (3 tries, short backoff)
  before failing the walk; a failed walk keeps everything absorbed and the
  next walk starts one page before the failure point (absorb is idempotent,
  so overlap is free). A laptop lid mid-walk costs a page, not the walk.
- Periodic refresh: one background goroutine walks the root context every
  `refresh` interval (same config key), so memory is warm before any read
  asks. Reads keep the current behavior — answer memory within
  `FirstAnswer`, never block on a walk.
- Existing promises hold: `List` stays non-authoritative, `ReadContent`
  before the first-ever completed walk stays `Unavailable` (with the cache
  file, that window is one process lifetime, ever), streaming first answers
  stay.

## The open bug: "loading gitlab..." outlives the answers

Observed on the affected home, 2026-08-31: the plugin logs
`"todos" answering with memory so far` (so `List` returned partial answers
within a second), yet the client face still shows "loading gitlab...". After
a laptop sleep the renderer logged `Uncaught (in promise) TypeError: network
error` and `[events] live updates disconnected`. The walk itself later
failed honestly on a connection reset (now retried under this design).

Hypothesis to verify FIRST, not assume: `loadGrid` fetches with
`context.Background()` and no deadline; if the wasm fetch dies with the
network (sleep, reset) without returning to Go, `gridInflight[id]` stays
latched and no refetch ever fires — "loading" forever with no error. The fix
must be for the class (bounded or reconnect-cleared in-flight fetches, error
surfaced), proven by a test that kills the connection under an in-flight
grid fetch (the browser-mode e2e suite can restart `gridwell serve` mid
fetch). If the root cause turns out to be something else, fix that; the
test-first rule in `CLAUDE.md` applies either way.

## Gates

`make check` in both repos; the gitlab seam tests extended to restart the
plugin over a kept `state_dir` and assert the listing answers with NO
re-walk; a browser-mode (`make check-web`) spec for the loading-bug fix;
`make check-e2e` under xvfb for the client change.

## Implementation prompt

Send the following to the implementation server.

---

Implement `docs/plugin-state.md` (read it first, in full) across two sibling
repos: `git@github.com:josephburnett/gridwell.git` and
`git@github.com:josephburnett/gridwell-plugins.git`, both on `main`. Clone
side by side; gridwell's Makefile builds plugin binaries from the sibling.
Read each repo's `CLAUDE.md` and follow it exactly: root-cause first, every
bug fix is a test that fails before and passes after, one logical change per
commit, comments true, no error swallowed.

Run as a team: a Fable coordinator with Opus workers.

- Worker 1 (gridwell): the `state_dir` mechanism — loader, `Home()`
  threading, plugintest, docs and decision-list amendments.
- Worker 2 (gridwell-plugins): the gitlab cache file, restored
  `doneComplete`, per-page retry and resume, the periodic refresher, and
  their unit tests.
- Worker 3 (gridwell): the "loading gitlab..." bug. Reproduce it first in
  the browser-mode e2e harness (kill the serve process under an in-flight
  grid fetch and watch `gridInflight`); only then fix, for the class. Do
  not start from the hypothesis — verify it.
- Coordinator: sequence worker 1 before worker 2's seam tests (they need
  `state_dir` in plugintest), integrate, then run the gates: `make check`
  in both repos, `make check-web`, and `make check-e2e` under xvfb
  (`xvfb-run -a`). The e2e gates rebuild at start; never edit sources while
  one runs.

Constraints: no wire/proto changes; no node-side database access from any
plugin; never auto-delete a state directory; the gitlab cache file writes
atomically. Push each repo's commits to `main` when its gates are green.
Report: per-commit summary, gate results, and for worker 3 the verified
root cause with the failing-first test named.

---
