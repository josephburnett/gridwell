# Plugins: the third-party door

An options document (2026-08-15) for the decoupling intent now in the
charter: the plugin system exists so **other people can build plugins**.
go-plugin was chosen for exactly the two things a stranger needs —
process isolation and a **separate dependency graph** (Go's own `plugin`
package offers neither; it demands the identical toolchain and module
graph). Everything else the seam happens to provide — id-space
isolation, a wire contract — could be had with coding discipline alone.

The problem this document exists to solve: **the separation erodes when
left to intention.** Host↔plugin and client↔plugin coupling has been
asked out of this codebase several times and has crept back each time,
in part because the included plugins live in the same repo and nothing
ever *exercises* the boundary — an in-tree plugin never pays for a
breach, so breaches are free until a stranger shows up. The fix is
machinery, not resolve. Options below; nothing here is decided.

---

## 1. The coupling inventory (audited 2026-08-15)

What crosses the boundary today, judged:

| Site | What it does | Verdict |
|---|---|---|
| `internal/plugin/registry.go` `TransitKind` | routing semantics by `kind == "ssh"` | **breach** — a third-party mount plugin can never be transit. Should be a wire declaration (see §4) |
| `internal/cli/sshmigrate.go` | the CLI imports `sshhost` and writes its DB (the #251 config→data migration) | **breach**, historically justified — the host owned the legacy config being migrated. Needs a retirement path (§4) |
| `internal/cli/init.go` ssh carve-out | refuses retired ssh config keys in the generic init door | **breach**, migration-era guard — ages out with sshmigrate |
| `client/wasm/palette_draw.go` `drawPluginGlyph` | glyph by kind switch (`fs`/`proc`/`localdb`, globe fallback) | **breach in shape** — degrades politely (strangers get the globe) but no declaration lets a plugin choose its face |
| `client/wasm/plugin_id.go` `pluginKind` | glyph kind from `Grid.source_kind`, falling back to the plugin-list kind | half-cured: `source_kind` IS a declaration; the fallback re-enumerates |
| `mobile/mobile.go` `inProcessFactories` | enumerates all four kinds with constructors | **legitimate** — a LEAF BINARY choosing what it ships is the one place enumeration belongs |
| `internal/cli/serve.go` `resolvePluginBinary` | binary by `gridwell-<kind>` naming | **fine** — convention, not enumeration; any kind resolves if the binary exists |
| `internal/store` → `client/markdown` (`AltFromSource`) | persistence imports the client tree | the same disease on a different axis (ARCHITECTURE §4.2's known wrinkle) — cure alongside |
| server/plugin **tests** importing `localdb` etc. | seam tests need a real plugin | **fine** — tests exercise the seam; the rule binds shipped binaries and the client |

What already works declaration-driven — the shapes to copy: Info
capabilities (`watch`, `writable`, `has_session`), `instance_grid_id`
(parameterized plugins), `Grid.source_kind` + `writable`,
`Tile.serves_page`, `Tile.text_presentation`. Every one of these is a
plugin telling the system what it is, instead of the system recognizing
a name.

---

## 2. What the door is made of (and what it lacks)

The contract a stranger builds against, today:

- `api/gridwell/v1/data.proto` — the whole interface, one service. gRPC,
  so **not Go-only**: any language behind go-plugin's documented
  handshake works.
- The spawn contract: `gridwell-<kind>` binary, config via
  `GRIDWELL_PLUGIN_CONFIG` (uuid + kind + db_file injected), go-plugin
  handshake. Small, but **documented nowhere a stranger would find it**
  — it lives in `internal/plugin/guest` and the four mains.
- Identity discipline: persist the injected uuid (pluginmeta), never a
  purely-numeric or slash-containing id segment.

Missing for a real stranger: a written spec of the above; a way to
declare transit-ness, a glyph, and init-time config validation; and any
proof the door still opens for a binary built outside this module.

---

## 3. Structural enforcement — the options

### Option A — the import-boundary lint (cheap, immediate)

A `make check` test that walks the import graph (`go list -deps`) and
fails if `cmd/gridwell`, `internal/server`, `internal/cli`,
`internal/node`, or `client/...` transitively imports
`internal/plugin/{localdb,fs,proc,sshhost}` (tests exempt; `mobile/`
exempt as a leaf binary). ~40 lines, zero workflow friction, and the
creep becomes a red build instead of a review argument.

*Limit:* it polices imports, not kind switches, and shared deps stay
shared — the dependency-graph half of go-plugin's value goes unexercised.
A companion grep-lint for kind literals outside plugin trees is possible
but blunt; the real cure for switches is retiring them (§4).

### Option B — the out-of-tree canary plugin (the "stranger test")

A toy plugin in its **own Go module** (its own `go.mod`, deliberately
divergent dependency versions, e.g. `test/thirdparty/gridwell-canary/`),
built and spawned by `make check-federation`: serve a trivial grid,
assert it lists, routes, reads, and mounts like any other. This is the
only option that *exercises* what go-plugin was chosen for — a separate
dependency graph crossing the door — and it doubles as the living spec
of the spawn contract (§2's missing document, as executable truth).

*Limit:* federation-gate cost (one more binary build per run); the
canary must be kept honest (never let it import the main module except
the generated proto — which forces the proto-module question in
Option C).

### Option C — separate Go modules for the included plugins

Each plugin (or all four together) becomes its own module; a `go.work`
stitches development; the main module physically cannot import them.
The full-strength version of the boundary.

*Costs, honestly:* the generated proto (`api/gen`) must become an
importable module of its own (or be published) — it is the one thing
both sides legitimately share; releases become multi-module
(tag/version choreography); atomic cross-cutting changes — which this
repo makes constantly (proto + server + client + plugin in one commit)
— now span modules, and `go.work` papers over that locally but CI and
releases feel it. This is heavy machinery to keep a promise that
Options A+B keep for a fraction of the cost. It earns its keep the day
a plugin actually wants a dependency the main module refuses, or moves
to its own repo.

### Option D — the two-binary (leaf enumeration) pattern

Already half-built by the mobile work, worth naming as the rule:

- `cmd/gridwell` — the HOST: spawns whatever binaries the config names,
  imports **zero** plugin implementations. (True today; Option A pins
  it.)
- `mobile/` — a BUNDLED leaf: imports select plugins in-process because
  iOS forbids fork/exec. Enumeration lives here, on purpose.
- Optionally later: `cmd/gridwell-all`, a desktop bundled main for
  single-file distribution, same pattern.

The rule this encodes: **enumeration is a leaf-binary privilege.**
Anyone can build their own bundled main with their own plugin set; the
host and the client never need to know. A `database/sql`-style
side-effect registration could sugar the leaf files, but with two leaf
binaries a plain map is clearer than an import-order protocol.

---

## 4. Retiring the standing breaches (declaration work)

Independent of A–D; each removes an enumeration by moving the fact to
the wire, the shapes §1 lists as already working:

1. **Transit → Info.** `InfoResponse.transit` declared by the plugin.
   The current comment argues transit must be config-time ("known while
   the remote is unreachable") — but the fact belongs to the LOCAL
   transport binary, which is alive even when its remote isn't; cache
   it from the spawn-time handshake exactly like identity. Any
   third-party mount plugin (an S3 mount, an IMAP bridge) then routes
   chains without the host learning its name.
2. **Glyph → declaration.** An Info (or root-grid) glyph hint — likely a
   small enum first (`folder`, `process`, `well`, `globe`, …), an emoji
   or blob later if wanted. `drawPluginGlyph` keeps its fallback globe;
   the switch on kind goes.
3. **sshmigrate → scheduled retirement.** The migration is a bounded
   legacy bridge (pre-#251 configs). Give it a sunset (one release with
   a loud "migrated, this path is removed next release" print), then
   delete the import — rather than generalizing machinery for a
   one-time event. init's ssh carve-out goes with it, or becomes a
   generic plugin-declared "refused init keys" if any other plugin ever
   needs one.
4. **`internal/store` → `client/markdown`**: move `AltFromSource` to a
   neutral package (`internal/doctype` already exists as the precedent
   home for exactly this).

---

## 5. A sequence, if the intent is all of it

Not a prescription — a cost-ordered path:

1. **A + the checklist line** (already in the charter): the boundary
   stops eroding this week.
2. **§4 retirements**: the standing enumerations go declaration-driven;
   after this, a kind string appears nowhere outside plugin trees and
   leaf binaries.
3. **B, the canary**: the door is now *proven* open to strangers on
   every federation run, and the spawn contract has an executable spec.
   Write the human-readable `docs/plugin-authoring.md` off the canary's
   back.
4. **C, separate modules**: only when a real dependency divergence or an
   out-of-repo plugin makes it pay. B will have already forced the
   proto-module question by then, which is most of C's groundwork.

The end state either way: the host knows *how to open the door*, the
wire knows *what came through it*, and nothing anywhere knows the
guests' names.
