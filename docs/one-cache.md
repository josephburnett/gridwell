# One cache per network seam

## Proposal

A cache earns its keep only across a network. Gridwell has two network
seams and one local one:

- Plugin ↔ its source (network): the plugin's own `state_dir` memory.
  Its problem, its policy (`docs/plugin-state.md`).
- Node ↔ remote node (network): `sourcecache` over `cache.db`. Prefetch,
  serve-stale-when-dark, the chip, the strip. The offline story.
- Node ↔ plugin (local socket): no cache. A call to a subprocess on the
  same machine is a function call; caching it papered over a missing
  supervision policy.

This amends the 2026-08-29 unification ("one engine in front of every
plugin as well as the transport"). The engine stays one engine; it just
fronts the one seam that needs it. `cache.db` keeps its name — after
this there is only one node-side cache — and its contract line becomes
"what a connection last answered."

## The rip-out (plugins uncached)

- `internal/node/node.go`: plugins are registered bare; only the
  transport is fronted. `openCache` and the close order are unchanged.
- Supervision replaces the dark-plugin window: a plugin subprocess that
  dies is respawned with backoff, and the down/up transition surfaces as
  the namespace's health event (the same `EventPluginHealth` the strip
  already renders). While down, calls fail honestly — no remembered
  answers. The registry is the one owner of "is this plugin alive."
- The event stream moves home: `pluginhost` (not a cache layer's side
  effect) declares `Watch` and serves the plugin namespace's stream —
  respawn health, and grid changes the adapter itself can see. The
  sourcecache layer's synthetic stream serves connections only.
- The plugin-namespace cache tests are reworked: dark-plugin tests
  become respawn tests; serve-first, revalidation, write-fold,
  rename-upsert, and cache-health tests keep their engine coverage with
  connection-shaped fixtures. The gitlab `state_dir` seam tests already
  prove plugin-side memory works uncached.
- `api/plugin/v1/plugin.proto`'s opening line still says "A plugin is
  stateless"; it gets the amended wording from `docs/plugin-state.md`.

## The kept cache, fixed

- Prefetch is dead at the doorstep: `Layer.Prefetch` starts with
  `Info`, which the transport does not implement, so the whole-mount
  offline warm silently does nothing. Fix for the class: the walk roots
  come from what a connection actually answers (`Handshake`), and a
  seam test puts a real `remote.Server` behind a prefetching layer —
  every existing prefetch test fakes the upstream, which is why this
  was not caught.
- `Grid.stale` means "this serve is a memory": stamped past the fresh
  window, and stamped inside it once the connection is known dark.
  Darkness is learned from any failed pass-through call or failed
  revalidation, cleared on the next success, and its discovery emits on
  the layer's stream so a client already holding the grid refetches.
  The two red gates assert the old synchronous meaning and are updated
  to poll past discovery: `test/connections/partition_test.go` and
  `apps/desktop/e2e-web/web-remote-menu.spec.ts` (#256, broken by
  9ad34354).

## The vocabulary

Two concepts, two words: a **node** (a running Gridwell) and a
**connection** (the configured edge to another node). "Federation" and
noun-"remote" named the same edge a third and fourth time and are
retired: `internal/remote` → `internal/connection`, `check-federation`
→ `check-connections`, the federation door → the connection door (the
same 0600 socket), docs and comments swept. "Remote" survives only as a
plain adjective. No wire change; `server.yaml` already says
`connections`.

## Gates

`make check` per commit; `make check-connections` (né check-federation)
for the supervision and prefetch work; `make check-web` for the #256
spec; `make check-e2e` for anything the client sees. The new prefetch
seam test and a respawn seam test are part of the program, not
follow-ups.

## Non-requirements

No wire/proto field changes. No `cache.db` rename or schema change. No
plugin-side changes (gridwell-plugins is untouched). No new client
state.
