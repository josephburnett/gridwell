# One node: finishing the v2 fold

Status: APPROVED 2026-08-29 (decisions §4: two files; rewrite old prefixes; rename to Handshake). Phases execute in order. Successor to `docs/v2-design.md` (which
executed the content-plugin half) — this finishes the other half, the
one the fold left behind: the node still *pretends* its own store and
its own transport are plugins, and a tile's arrangement is stored in
three different tables by three different engines.

Owner direction (Joe, 2026-08-28): *one server, one config, one id, one
local database; many `connections:`; many `plugins:` that are ONLY
content plugins. Stop preserving everything — it's a single-user app.*

## 1. What is actually wrong (the inventory, compressed)

Three audits mapped the tree (config and CLI, node and server, client and wire).
The half-fold shows up as six leftovers:

| # | Leftover | Where |
|---|---|---|
| L1 | **Two ids for one thing.** `node_id` (yaml only, qualifies exactly one synthetic grid `<node_id>/0`) and the home store's plugin id (in every stored reference). Nothing on disk references the node id — 0 rows across all DBs and caches. | `config.go:381`, `nodegrid.go:37` |
| L2 | **Natives dressed as plugins.** `home`/`remote` are `plugins:` entries with ids, `kind`, an `Info` handshake, `pluginmeta` verify, a gRPC loopback hop to code in the same process (`compose.ServeInProcess`), and typed config smuggled through a `map[string]string` (`connections_json`). `IsNative` is the switch. | `node.go:100-109`, `loader.go:116-137`, `nativeremote.go:46-66` |
| L3 | **The node grid.** An in-process fake plugin serving one read-only grid with its own state file (`node-view.json`) — a second copy of "a grid's root view". The client lands there only as a fallback; mounts land on the remote's home root anyway. | `nodegrid.go`, `client/wasm/plugin_id.go:47` |
| L4 | **A transport id between the node and its connections.** Every remote reference is `<remote-id>/<conn>/<plugin>/<tile>`; the remote-id segment exists only because remote was once a stranger. Connections then get an "instance grid" and synthesized `Kind:"instance"` rows — the dead #251 picker's shape. | `remote.go:115-146`, `connect_handler.go:242-290` |
| L5 | **Arrangement stored three ways.** `store.tiles` (home), `layout.layout` (plugin memory), `remote.ssh_connections` (connection wells) each carry `x y w h view_x view_y view_zoom …` with their own placement code; the mount cache mirrors a fourth. This is the "layout code repeated" the v2 job was supposed to end. | `schema.go:121`, `layout.go:78`, `remote/db.go:63` |
| L6 | **Migration scaffolding still standing.** `kindmigrate` + `RenamedKinds`, flat `bind:`/`password:`/plugin-flag folds, `parity` + `convert-scope.txt`, `gridwell-all` (kept alive only by the parity gate), the dead `session` table, `system.plugin_uuid` (third id copy), `ConfigurePluginId`/`CreatePluginWell`, `instance_grid_id`, `node_uuid` on the wire (no reader). Plus a live bug: the desktop's first-run `init --kind local` uses a retired kind and fails (`sidecar.ts:191`). | `kinds.go`, `config.go:246-264`, `cli/parity.go` |

## 2. The target

```
~/.gridwell/
  server.yaml      one config
  gridwell.db      one durable DB: home content + every namespace's arrangement
  cache.db         one disposable DB: mount cache (excluded from backup)
  web-password     0600, minted
  federation.sock  0600, runtime
```

### 2.1 One identity

`id:` in server.yaml is the node **and** the home. It prefixes every
local reference (`<id>/12`) and every connection (`<id>/<conn>/…`).
Joe's home keeps `1b467bbd…` (128 tiles already carry it); the old
`c69b61…` node id is dropped — nothing references it.

### 2.2 One config

```yaml
id: 1b467bbd65466256f8a64c538cabdac8      # minted by serve if absent
web:        { bind: localhost:10010 }
federation: { socket: ~/.gridwell/federation.sock }   # default, optional
disable_shells: false

connections:                              # remote nodes
    - name: geneva                        # immutable namespace segment
      label: Geneva
      host: geneva.gila-lionfish.ts.net
      user: joe
      addr: /home/joe/.gridwell/federation.sock
retired_names: [y4l19nr, mebkyaa]

plugins:                                  # content plugins ONLY
    - id: umyd7dx                         # minted by serve if absent
      kind: fs
      label: Home dir
      config: { root: /home/joe }
```

Gone: `node_id`, `kind: home/remote` entries, `name: ""`, the retired plugin flag,
flat `bind:`/`password:`, `kindmigrate`. **Serve mints** any missing
`id` (node or plugin) and writes it back — the only config write. So
`gridwell init` goes away: create the file, run serve. The desktop
first-run bug disappears with it.

### 2.3 Namespaces, not plugins

The registry holds **namespaces**; each is a Go value implementing the
Gridwell service directly (no in-process gRPC hop):

| Namespace | Segment | Backed by |
|---|---|---|
| home | `<id>` | the store |
| connection | `<id>/<name>` | the transport (dial + mount cache) |
| plugin | `<plugin-id>` | the pluginhost adapter over a plugin.v1 subprocess |

`<id>/12` vs `<id>/geneva/…` is unambiguous by the standing id-shape
rule (tile ids numeric, segments letter-leading). Routing: first
segment → home or plugin; `<id>/<letters>` → that connection. "Transit"
is the namespace's *type*, not an `Info` declaration. `IsNative`,
`nativeFactories`, `compose.ServeInProcess`/`NativeFactory`, `local.Info`,
`remote.Info`, `pluginmeta` on natives, `connections_json` — deleted.

### 2.4 The handshake replaces the node grid

`ListPlugins` → `Handshake` (rename the RPC or keep the name; the shape
is what matters):

```
id, home_grid_id, home_view{x,y,zoom}, shells_disabled, content_token,
plugins[]     {uuid, label, root_grid_id, root_view, glyph, info_error, menu_entries…}
connections[] {name, label, root_grid_id (learned), health}
```

Home is a **field**, not "the first row with a root". The + menu top
row = plugins ∪ connections. The node grid, `node-view.json`,
`NodeRootView*`, `node_uuid`, `Grid.source_kind == "node"`,
`isNodeGridPane` and its six readers, `thLauncher`, `rpc.HomeGrid`'s
derive rule — deleted. Federation lands on the remote's `home_grid_id`
(what already happens; the node grid was only the rootless fallback).

### 2.5 Connections are config + a link

A connection has no instance grid, no well row, no synthesized
"instance" plugin. It is a menu row from config; the user drags it to
make an ordinary link tile in their own grid. What the node remembers
per connection: the learned `remote_root` and the tombstone — one small
table. `ssh_connections`' placement/framing columns, `instanceRows`,
`InstanceGridId`, `Kind:"instance"`, `ConnSpec`/`Params` duplicating
`ConnectionConfig` — deleted.

Stored references `i9sm6ff/ltvv2f9/…` (two wells) get a one-time prefix
rewrite to `<id>/ltvv2f9/…` in the converter (§3) — mechanical, so it
costs nothing to keep them.

### 2.6 One arrangement engine, one DB

`gridwell.db`:

```
grids  (id, ns, context_key, version, root_view…)     -- ns='' = home
tiles  (id, grid_id, ns, key, kind, x y w h, view_*, text_*, content_zoom,
        blob_id, version, tombstoned, …)
blobs  (id, hash, data, media_type, refcount)
connections (name, remote_root, deleted)
```

Every tile everywhere — home content, an fs entry, a gitlab todo — is a
row in **one** `tiles` table, placed and framed by **one** engine. For a
home tile `key=''` and content is `blob_id`; for a plugin tile `key` is
the plugin's stable key and content is fetched. `internal/layout`'s
reconcile (listing → mint ids / tombstone / place first-sighted) becomes
a function over this table, and the store's own placement code is that
same function. `remote.ssh_connections`, `layout.idmap/layout/contexts`,
per-`db/<id>/` directories — deleted. `cache_listings` and the mount
cache tables move to `cache.db`, keyed by namespace.

This resets the "storage format is frozen" rule at one cut: a one-shot
converter reads today's four files and writes the one DB; the format
contract starts again at v1 of this schema.

### 2.7 Delete list (L6)

`kindmigrate.go`, `kinds.go`, `cli/init.go`, `cli/parity.go`,
`convert-scope.txt`, `apps/gridwell-all` + `make check-parity`, the
`session` table, `system.plugin_uuid` + `SetPluginID`,
`ConfigurePluginId`/`CreatePluginWell`, `Tile.configure_plugin_id`,
`instance_grid_id`, `node_uuid`, `PluginInfo.kind` (client never reads
it; e2e drivers switch to `home_grid_id`), every "legacy"/"#251"/
"picker"/"fold" comment the inventories listed.

## 3. Phases

Each phase is a green tree with its gates and a converter step where
data moves. Order chosen so every phase leaves fewer copies than it
found.

| Phase | Lands | Deletes |
|---|---|---|
| **P1 identity + config** | new yaml shape; `id` = home id; serve mints ids; desktop first-run = serve | `node_id`, `init`, `kindmigrate`, `kinds.go`, all folds, `parity`, `gridwell-all` |
| **P2 handshake, no node grid** | `home_grid_id` on the wire; client anchors on it | `nodegrid.go`, `node-view.json`, `isNodeGridPane` + readers, `rpc.HomeGrid`, `node_uuid` |
| **P3 namespaces** | registry of Go values; connections at `<id>/<conn>`; prefix rewrite | `IsNative`, in-process gRPC hop, `Info` on natives, `pluginmeta` on natives, `connections_json`, instance grid + rows, `ssh_connections` presentation columns |
| **P4 one DB** | `gridwell.db` + `cache.db`; one `tiles` table; one engine; converter | `db/<id>/` dirs, `layout.*` tables, `remote.ssh_connections`, `session`, `plugin_uuid` |
| **P5 words + docs** | README/ARCHITECTURE/CLAUDE rewritten to this model; vocabulary lint gains `native`, `instance`, `loadout` | the stale owner decisions (home-is-first-plugin, node-grid-is-federation-surface, #199, #251 remnants) |

P4 is the largest and the one that pays the "layout repeated" debt; P1–P3
make it small by removing everything that isn't a tile first.

## 4. Decisions needed from the owner

1. **Two files (`gridwell.db` + disposable `cache.db`) or literally one?**
   Recommendation: two — the cache is excluded from backup today for a
   reason and it can be deleted freely.
2. **Rewrite the two stored `i9sm6ff/…` references, or let them dangle?**
   Recommendation: rewrite — it's a prefix rename inside the converter.
3. **Keep `ListPlugins` as the RPC name or rename to `Handshake`?**
   Recommendation: rename; the old name is the last place "everything is
   a plugin" would survive.
