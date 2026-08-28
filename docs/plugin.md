# Plugins: the third-party door — the plan of record

Decided 2026-08-15 (supersedes the options draft of this file; the
charter carries the standing rule). The plugin system exists so OTHER
PEOPLE can build plugins: go-plugin was chosen for process isolation AND
a separate dependency graph. That separation eroded repeatedly when left
to intention, so it becomes STRUCTURE — modules whose arrows make the
breaches unrepresentable — and it is EXERCISED on every build: the
included plugins themselves become the strangers.

## The decided objectives

- ONE gRPC interface for plugins and the server (the proto stays the
  contract; nothing decays into a Go-interface side channel).
- An **api library** a plugin author needs and nothing more — and the
  composition sugar: a binary composer says `InProcess(factory)` or
  `Command("gridwell-plugin-fs")` and the server cannot tell which it
  got. In- vs out-of-process is the COMPOSER's choice, invisible above.
- A **server library**; **apps** that compose it; **plugins** as their
  own modules importing only the api — the in-repo "examples" every
  stranger copies.
- The structure codified and TESTED: wrong arrows fail the build.

Decisions taken in review (Joe, 2026-08-15):
- Plugin binaries rename to `gridwell-plugin-<kind>`. No fallback lookup
  — single user, clean cut.
- The wasm client + web/ stay in the SERVER module (strangers write
  plugins, not clients; the client is the server's embedded face).
- `internal/store` moves into the localdb plugin module — it is
  localdb's persistence. `store.NewShortID` + the id-shape validation
  lift into the api module: id shape is CONTRACT, not storage.
- ASSUMPTION to confirm: the pre-#251 sshmigrate bridge (the one
  host→plugin-impl import) is DELETED outright, not sunset — every
  existing home is already migrated. init's ssh carve-out goes with it.

## The target tree

```
gridwell/
  go.work                 dev stitching; CI also builds each module ALONE
  api/                    MODULE github.com/josephburnett/gridwell/api
  server/                 MODULE .../server   (the node as a library + its client)
  plugins/
    localdb/              MODULE .../plugins/localdb   (absorbs internal/store)
    fs/  proc/  ssh/      MODULES, same shape
  apps/
    gridwell/             MODULE — the stock HOST binary
    gridwell-all/         MODULE — the bundled example binary
    mobile/               the gomobile leaf (bundled by necessity: iOS)
    desktop/              electron shell (npm, unchanged)
  test/
    boundary/             the codified arrows (see below)
    federation/           the external-spawn gate (exists; binaries renamed)
```

### The api module (dep-lean — this graph is inherited by every plugin ever written)

```
api/
  gridwell/v1/            the proto + buf config
  gen/                    generated Go (moves from api/gen)
  guest/                  the external-plugin main harness (go-plugin serve,
                          handshake, config env) — moves from internal/plugin/guest
  compose/                the sugar: Loadout = InProcess(Factory) | Command(name);
                          spawn/dial/in-process serve behind ONE constructor,
                          so composers choose and callers can't tell
  idshape/                NewShortID + the id validity rules (no '/', never
                          purely numeric, leading letter)
```
Allowed deps: grpc, protobuf, go-plugin. NOTHING else — pinned by test.

### The server module

Today's `internal/{server,node,rpc,plugin(host side: registry, loader,
mountcache),config,cli,dbformat,doctype,...}` plus `client/` and `web/`.
It knows how to OPEN the door (spawn a Command, accept an InProcess
loadout) and never what's behind it.

### A plugin module (the example shape)

```
plugins/fs/
  go.mod                     requires .../api ONLY
  provider/ fsfile/ ...      the implementation
  cmd/gridwell-plugin-fs/  main: guest.Serve(provider.New(...))
```
`plugins/gitlab` (2026-08-25) is the first provider written AGAINST the
v2 door rather than ported to it: a stateless projection of a GitLab
account's to-do list — root context = weeks, week context = the todos
created that week as page tiles serving their own HTML; completion is
derived from absence in the pending set, listings are non-authoritative
and Probe never says GONE, so the node's read-through cache keeps every
todo the user has seen. Config:

```yaml
- id: <minted>
  name: todos
  kind: gitlab
  config:
    url: https://gitlab.com          # optional
    token_file: /home/me/.gitlab-token   # read_api PAT; a file path, never a value
    refresh: 30s                     # optional re-walk window per context
```

(2026-08-24: the legacy full-`gridwell.v1` fs/proc plugins retired with
the v2 cutover. 2026-08-27: the `gridwell.v1` subprocess door itself —
`guest.Serve`, `compose.Command`, the `provider: true` flag, the second
init door — retired: a plugin IS a content provider, every non-native
kind spawns `gridwell-plugin-<kind>`, and `local`/`remote` are the
node's own kinds. This document's remaining "gridwell-plugin-*" wording
is the 2026-08-15 plan as executed; the current door is
`docs/plugin-authoring.md`.)
localdb additionally absorbs `internal/store` (+ its CLAUDE.md contract,
unchanged) and the tmux/shellsvc machinery its binary owns.

### The apps

- `apps/gridwell`: server + api. ZERO provider imports — the host spawns
  `gridwell-plugin-*` binaries named by config/PATH. This is what dist
  ships today, minus the knowledge of who it ships with.
- `apps/gridwell-all`: the bundled example — same server, the shipped
  providers compiled in as `plugin.Factory` entries. Exists to
  PROVE the door (check-parity runs the browser suite against it) and
  as the template for anyone composing their own.
- `apps/mobile` (the Go bind in `mobile/` moves here or stays — leaf
  either way): the bundled leaf iOS forces.

## The arrows, and how they are enforced

```
plugins/*  →  api                      (and NOTHING else of ours)
server     →  api
apps/gridwell      →  server, api      (NO plugin modules)
apps/gridwell-all  →  server, api, plugins/*     (leaf enumeration — legal HERE)
apps/mobile        →  server, api, chosen plugins (same)
api        →  nothing of ours
```

`test/boundary` codifies it:
1. **Arrow lint**: `go list -deps` per module; any import outside its
   allowed set fails. (Tests are exempt; they exercise seams.)
2. **api dep budget**: api's module graph pinned to the allowed three;
   a new dependency is a loud, deliberate diff.
3. **Standalone builds**: CI builds every module WITHOUT go.work — the
   proof each plugin really stands alone on the published-shape api.
4. **Composition parity**: the e2e suite runs against `gridwell` (all
   external) and `gridwell-all` (all in-process); identical behavior is
   the pin that the compose sugar hides the process boundary.
5. **The federation gate** keeps spawning the renamed binaries — the
   external path stays exercised end to end.

Versioning: go.work for development; the app modules carry permanent
`replace` directives for in-repo siblings (legitimate for applications —
nobody imports the apps as libraries). The api module gets prefixed tags
(`api/v0.x.y`) only when an out-of-repo consumer exists; until then the
replace graph is the whole story.

## The coupling inventory (audited 2026-08-15 — stage 1's worklist)

| Site | Coupling | Fix |
|---|---|---|
| `internal/plugin/registry.go` `TransitKind` | routing semantics by `kind == "ssh"` | Info declaration (`transit`), cached from the spawn handshake |
| `internal/cli/sshmigrate.go` | the CLI imports `sshhost` and writes its DB (pre-#251 bridge) | DELETE (flagged assumption: every home is migrated) |
| `internal/cli/init.go` | ssh-specific config-key refusal in the generic init door | goes with sshmigrate |
| `client/wasm/palette_draw.go` `drawPluginGlyph` | glyph by kind switch (fs/proc/localdb) | Info glyph declaration; globe stays the fallback |
| `client/wasm/plugin_id.go` `pluginKind` | falls back from `source_kind` (a declaration — fine) to the plugin-list KIND | reads the glyph declaration instead |
| `internal/store` → `client/markdown` | persistence imports the client tree (`AltFromSource`) | derivation moves to `internal/doctype` |
| `mobile/` factories; future `apps/gridwell-all` | enumerate plugin kinds | LEGITIMATE — leaf binaries; the only legal place |
| seam tests importing plugin impls | tests exercise the seam | LEGITIMATE — exempt from the arrow lint |

## Status (2026-08-16): EXECUTED — all five stages landed

Stages 1–5 are on main (`3b916a5..` through the codify commit): the
couplings retired, the api module carved (gen + guest + compose +
idshape + gwerr + rpc + dbformat + pluginmeta + panelayout), every
plugin its own module with `gridwell-plugin-<kind>` binaries, the stock
host (`apps/gridwell`) + bundled example (`apps/gridwell-all`) + mobile
as leaf composers, and the structure codified: `test/boundary` (arrows +
api dep budget, in `make check`), standalone module builds (in `make
check`), `make check-parity` (the browser suite against gridwell-all —
in/out-of-process indistinguishable), the federation gate on the
renamed externals, and `docs/plugin-authoring.md` for strangers.

Deviations from the letter of the plan, each deliberate:
- The root module IS the server library (no `server/` directory move —
  identical arrows, a tenth of the churn). `mobile/` stayed at its path
  as its own module rather than moving under `apps/`.
- Shared neutral homes became NESTED modules in place
  (`internal/doctype`, `plugins/griddb`) instead of relocating; the api
  absorbed the contract-shaped ones (including `modernc.org/sqlite` in
  its budget for `pluginmeta`/`dbformat` — pinned, documented in the
  budget test).
- The root module still REQUIRES plugin modules for its seam TESTS
  (server tests build real plugins); the arrow lint polices imports of
  non-test packages, which is the promise that matters — a leaf's
  `replace` ceremony and this test-require are the two places module
  mechanics show through.
- Cross-seam tests that build a server around a plugin moved to the
  server side (they are the server's seam tests).

## Execution — staged, each stage green and pushed alone

1. **Retire the couplings in place** — the inventory above, top to
   bottom (before any moves — smallest diffs, and the moves then carry
   no breaches with them):
   - `TransitKind` → an Info declaration (`InfoResponse.transit`): the
     transit fact belongs to the LOCAL transport binary, which is alive
     even when its remote isn't; cached from the spawn handshake like
     identity. The registry stores what the plugin declared.
   - Plugin glyph → an Info declaration (`glyph` enum: folder, process,
     well, globe fallback); `drawPluginGlyph`'s kind switch goes.
   - DELETE sshmigrate + init's ssh carve-out (the assumption above).
   - `store.AltFromSource` dependency: derivation moves to
     `internal/doctype` (the decided neutral home).
2. **Carve the api module**: `api/go.mod`; move guest → `api/guest`,
   mint `api/compose` (Loadout; the loader learns to accept one),
   `api/idshape`; go.work + replaces; dep-budget test lands WITH it.
3. **Move the plugins out**: `plugins/{localdb,fs,proc,ssh}` as modules
   (localdb takes `internal/store`); binaries rename to
   `gridwell-plugin-<kind>` (Makefile, dist/electron-builder, sidecar,
   GRIDWELL_PLUGIN_DIR conventions, federation fixtures). No fallback.
4. **Shape the server module + apps**: `server/` module; `cmd/gridwell`
   → `apps/gridwell` (host, plugin-free); mint `apps/gridwell-all`;
   mobile rewires to the api compose path.
5. **Codify**: `test/boundary` (arrows, budget, standalone matrix),
   e2e composition parity, gates updated; `docs/plugin-authoring.md`
   written off the fs example's back.

Each stage keeps every gate green (`check`, electron, e2e, web,
federation). The storage format, wire contract, and every id are
untouched throughout — this is a re-shelving, not a migration; homes
and DBs never notice.

## What this buys, restated

The host knows how to open the door. The wire knows what came through
it. Only leaf binaries know the guests' names — and the four guests we
ship are held to the same door as everyone else, on every build.
