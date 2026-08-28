# Writing a Gridwell plugin

A plugin is a separate program (or a compiled-in factory) serving the
`plugin.v1` gRPC service (`api/plugin/v1/plugin.proto`): it answers
in its own stable string KEYS — a path, a message-id, a todo id — and
never sees ids, layout, or a database. The node mints numeric ids
against your keys, stores placement and framing in its own memory DB,
and serves the full Gridwell surface to clients on your behalf. The host
never knows your name: every behavior rides a declaration on the wire.

The in-repo plugins (`plugins/{fs,proc,gitlab}`) are held to the same
door you are — each its own Go module importing only the api. `gitlab`
is the worked example of a plugin written against this interface rather
than ported to it; `fs` is the fullest surface.

## The contract

- **Module**: depend on `github.com/josephburnett/gridwell/api` only.
  Its packages: `gen/plugin/v1` (implement
  `PluginServer`), `guest` (your main: `guest.Serve`,
  `guest.Config`), `compose` (how a node loads you), `gwerr` (the error
  vocabulary). Not Go? The service is plain gRPC behind hashicorp
  go-plugin's documented handshake — any language works.
- **Binary name**: `gridwell-plugin-<kind>` beside the `gridwell`
  binary, on `GRIDWELL_PLUGIN_DIR`, or on PATH (or an explicit
  `binary:` in server.yaml). Registration is one line of config:
  `gridwell init --kind <kind> --name <label> [--config k=v]`.
- **Main**: `guest.Serve(yourImpl)`. Config arrives as a JSON
  map in `GRIDWELL_PLUGIN_CONFIG` (`guest.Config()`): your server.yaml
  keys plus the injected identity (`uuid`, `kind`). The same map reaches
  an in-process factory (`plugin.Factory`, the bundled-leaf
  shape) — one config vocabulary, both process shapes.
- **Keys are forever**: a key names the same logical thing for the
  life of the plugin. Changing your key scheme orphans every stored
  reference, exactly as re-minting ids would.
- **Unimplemented is polite**: a minimal plugin is Info + List +
  ReadContent. Search = no results, ServeContent = 404, Watch = no
  events, WriteContent = read-only, GetPreview = no thumbnail, Delete =
  refused.
- **Errors**: transport-shaped failures (Unavailable, DeadlineExceeded)
  mean "not right now" — the node serves its remembered listing,
  stamped stale; coded answers mean what they say. Never answer
  NotFound for something that exists but is unreachable.

## Declarations (Info) — how you tell the system what you are

`InfoResponse` is your one handshake: `kind`, `display_name`, `glyph`
(`folder`/`process`/`well`/empty for the globe), `root_context` (your
landing grid's key; empty = no landing grid), `watch`, `writable`, and
`menu_entries` (the + menu additions the node stamps onto your grids).

## Listings and existence

`List` enumerates one context. Say whether it is `authoritative`: a key
absent from an authoritative listing is GONE (a readable directory);
absent from a non-authoritative one means "not seen this pass" (the
process table, a todo list) — the node keeps serving remembered entries
until `Probe` answers GONE. A `placement_hint` seeds an entry's FIRST
placement only; the user's arrangement wins from then on. An entry with
`serves_page` presents its `ServeContent` HTML on descent, sandboxed by
the node.

## Running in-process instead

A bundled binary (`apps/gridwell-all` is the template; the mobile bind
is the other leaf) skips the subprocess: provide a
`plugin.Factory` and hand it to `cli.Main` — the node cannot
tell the difference. This is how plugins run on iOS, where fork/exec
does not exist.

## The gates that keep this true

`test/boundary` fails the build if any host/client package imports a
plugin implementation, or the api gains a dependency outside its
budget; `make check` builds every module standalone and spawns the real
fs plugin binary through the production loader (`internal/plugin`);
`make check-federation` spawns the real binaries through a real ssh
tunnel. Your plugin gets the same door on every build — because ours
do.
