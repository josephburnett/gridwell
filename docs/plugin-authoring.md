# Writing a Gridwell plugin

A plugin is a separate program (or a compiled-in factory) serving ONE
gRPC service — the same 17-RPC contract the server, the browser client,
and every federated node speak. The host never knows your name: every
behavior you want rides a declaration on the wire.

The four in-repo plugins (`plugins/{local,fs,proc,remote}`) are held to
the same door you are — each is its own Go module importing only the
api — so `plugins/fs` is the worked example for everything below.

## The contract

- **Module**: depend on `github.com/josephburnett/gridwell/api` only.
  Its packages: `gen/` (the generated service — implement
  `gridwellv1.GridwellServer`), `guest` (your main), `compose` (how
  binaries load you), `idshape` (id minting/validity), `gwerr` (the
  error vocabulary every transport classifies), `dbformat` +
  `pluginmeta` (the persistence promise's mechanics, if you use SQLite),
  `rpc` (Go-side types + converters), `panelayout` (the workspace blob
  format, if you serve pane tiles). Not Go? The service is plain gRPC
  behind hashicorp go-plugin's documented handshake — any language
  works.
- **Binary name**: `gridwell-plugin-<kind>` on the PATH (or an explicit
  `binary:` in server.yaml). Registration is one line of config
  (`gridwell init --kind <kind> --name <label>`).
- **Main**: `guest.Serve(yourImpl)`. Config arrives as a JSON map in
  `GRIDWELL_PLUGIN_CONFIG` (`guest.Config()`): your server.yaml keys
  plus the injected identity — `uuid` (your durable routing id — persist
  it; `pluginmeta` does this for SQLite), `kind`, and `db_file` (your
  derived DB path). The same map reaches an in-process factory
  (`compose.Factory`) — one config vocabulary, both process shapes.
- **Ids**: yours are plugin-local; the host prefixes them. A namespace
  segment you mint (a sub-connection, an instance) must satisfy
  `idshape.ValidateSegment` — no `/`, never purely numeric — and an id
  is immutable once minted.
- **Errors**: answer with `gwerr` sentinels (wrapped is fine) so every
  transport classifies you identically. Transport-shaped failures
  (Unavailable) mean "not right now" and degrade (the mount cache, the
  offline clone); coded answers mean what they say — never answer
  NotFound for something that exists but is unreachable.

## Declarations (Info) — how you tell the system what you are

`InfoResponse` is your one handshake; the host caches it. Declare:
`root_grid_id` (your landing grid) or `instance_grid_id` (parameterized:
the picker opens instead), `writable`, `watch` (you implement
Subscribe), `transit` (your ids are chains from another node — you get
the mount cache in front of you), `glyph` (`folder`/`process`/`well`/
empty-for-globe), `create_schemas`, `scratch_grid_id`. The client
renders strangers politely: unknown glyphs are globes, missing
capabilities gray out.

## Behavior rules the seam tests will hold you to

- A failed read never deletes state — only a definite GONE does (the
  fs/proc sweep rule, invariant I12).
- `version` bumps on content edits only; framing writes (`view_*`,
  scroll) never bump it.
- `WriteContent` commits at close: a broken stream leaves the old value
  byte-for-byte.
- The storage format promise, if you persist: additive-only evolution
  (`dbformat` gives you the versioning engine; `plugins/local/store`'s
  CLAUDE.md is the worked contract).

## Running in-process instead

A bundled binary (see `apps/gridwell-all`, the template) skips the
subprocess: provide a `compose.Factory` and hand it to `cli.Main` — the
server cannot tell the difference (the parity gate proves it). This is
also how plugins run on iOS, where fork/exec does not exist.

## The gates that keep this true

`test/boundary` fails the build if any host/client package imports a
plugin implementation, or the api gains a dependency outside its budget;
`make check` builds every module standalone; `make check-parity` runs
the browser suite against the bundled binary; `make check-federation`
spawns the real binaries through a real ssh tunnel. Your plugin gets the
same door on every build — because ours do.
