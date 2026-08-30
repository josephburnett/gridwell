# Writing a plugin

A plugin is a separate program (or a compiled-in factory) that serves the
`plugin.v1` gRPC service (`api/plugin/v1/plugin.proto`). It answers in its
own stable string keys — a path, a message id, a todo id — and never sees
ids, layout, or a database. The node mints ids against your keys, stores
placement and framing, and serves the full Gridwell surface on your behalf.
The host never knows your name; every behavior rides a declaration on the
wire.

The in-repo plugins (`plugins/{fs,proc,gitlab}`) use the same door: each is
its own Go module importing only the api. `gitlab` is the worked example.
`fs` is the fullest surface.

## The contract

- **Module**: depend on `github.com/josephburnett/gridwell/api` only. Its
  packages: `gen/plugin/v1` (implement `PluginServer`), `guest` (your main:
  `guest.Serve`, `guest.Config`), `compose` (how a node loads you), `gwerr`
  (the error vocabulary). Not Go? The service is plain gRPC behind
  hashicorp go-plugin's handshake.
- **Binary**: `gridwell-plugin-<kind>` beside the `gridwell` binary, on
  `GRIDWELL_PLUGIN_DIR`, on PATH, or named by `binary:` in `server.yaml`.
  Register with a `plugins:` entry (`kind`, optional `label`, your `config`
  keys). The first serve mints the entry's id.
- **Main**: `guest.Serve(yourImpl)`. Config arrives as a JSON map in
  `GRIDWELL_PLUGIN_CONFIG` (`guest.Config()`): your keys plus `uuid` and
  `kind`.
- **Keys are forever**: a key names the same thing for the life of the
  plugin. Changing your key scheme orphans every stored reference.
- **Unimplemented is fine**: a minimal plugin is `Info` + `List` +
  `ReadContent`. Search = no results, ServeContent = 404, Watch = no
  events, WriteContent = read-only, GetPreview = no thumbnail, Delete =
  refused.
- **Errors**: transport-shaped failures (Unavailable, DeadlineExceeded)
  mean "not right now" and the node serves what it has, stamped stale.
  Coded answers mean what they say. Never answer NotFound for something
  that exists but is unreachable.

## Info

`InfoResponse` is your one handshake: `kind`, `display_name`, `glyph`
(`folder`/`process`/`well`/empty), `root_context` (your landing grid's key;
empty = none), `watch`, `writable`, and `menu_entries` (the + menu additions
the node stamps onto your grids).

## Listings

`List` enumerates one context. Say whether it is `authoritative`: a key
absent from an authoritative listing is gone; absent from a non-authoritative
one means "not seen this pass", and the node keeps the entry until `Probe`
answers GONE. A `placement_hint` seeds an entry's first placement only. An
entry with `serves_page` presents its `ServeContent` HTML on descent,
sandboxed by the node.

## Gates

`test/boundary` fails the build if a host or client package imports a plugin
implementation, or the api gains a dependency outside its budget. `make
check` builds every module standalone and spawns the real fs plugin through
the production loader. `make check-federation` spawns the real binaries
through a real ssh tunnel.
