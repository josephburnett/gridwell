# Gridwell architecture

Gridwell is a personal space made of tiles on an infinite 2D grid. There are
four primitives: text, url, shell, and well (a tile that holds another grid).
A pane tile holds a split-pane layout. Grids nest without limit, and they
federate across plugins and machines. The one rule: things stay as you left
them.

This doc says how the machine works. `CLAUDE.md` says how to change it.

## Layers

```
Electron main       apps/desktop/src/main     live url pages as native views
     │ IPC
wasm client         client/wasm + client/*    canvas, panes, gestures, framing
     │ Connect-RPC (+ a WebSocket for shells)
router              internal/server           stateless; routes by id
     │ Go call (namespace.Namespace)
namespaces          home · plugins · connections
     │
store               internal/local/store      gridwell.db
```

Inside the node a namespace is a Go interface (`internal/namespace`). There
are exactly two gRPC hops, both across a real process boundary: the plugin
subprocess (`plugin.v1`) and the connection door (`gridwell.v1`). Connect
over HTTP is the third codec, and it crosses to the browser.

## Ids

An id is a chain of segments, one per hop: `<node>/<conn>/<remote-node>/<tile>`.
`Server.resolve` peels the first segment and routes on its shape:

- the node's own id followed by digits: home
- the node's own id followed by a letter-leading segment: a connection
- anything else: a plugin

The remainder passes through untouched, so any depth of mounting routes the
same way. Ids going out are re-qualified once (`qualifyTilesFor`): a leaf
prefixes and derives `Tile.reference`; transit prepends one segment and
trusts the wire bits (`rpc.TransitQualifyTiles`). Qualification clones —
that clone is the whole protection for messages shared by pointer.

A segment has one of three shapes, and `rpc.ShapeOf` is the one classifier
every reader shares. Plugin and node ids are 7-char lowercase base36 with a
leading letter (`idshape.NewShortID`); a tile is a store row id in decimal
digits, or a KEY FORM — `~` followed by the plugin key's unpadded base64url
(`rpc.KeyTileID`), which names a tile by what it is rather than by a row.
The leading byte separates all three, so a URL path and the router's peel
both tell a namespace segment from a tile id without asking anyone. Ids are
never reassigned.

## The contract

`api/gridwell/v1/data.proto` is the one description of the wire and the
record shapes. Everything else derives from it:

- `api/rpc/wire_gen.go` (the Go record types and conversions) is generated
  by `api/rpc/internal/gen`; `make proto-check` fails when it is stale.
- `internal/local/store/columns.go` is the one description of a stored
  column. It renders the DDL and drives every SELECT, scan, clone INSERT,
  and rebuild copy list. `TestDescriptorMatchesProto` pins the descriptor
  to the proto in both directions.

| Group | Methods |
|---|---|
| Lifecycle | `Info`, `Probe`, `Handshake` |
| Reads | `GetGrid`, `GetTile`, `GetTilePreview`, `Search` |
| Content | `ReadContent`, `WriteContent` — the one way bytes move. Versioned. A write commits at close; a broken stream leaves the old value. |
| Web content | `ServeContent` — behind `/content/<token>/<tile-id>/<subpath>`. Sandboxed (`CSP: sandbox allow-scripts`), gated by the content token, never the cookie. |
| Framing | `SetFraming` — the one framing write |
| Mutations | `CreateTile`, `SetTile` (one op per call), `PlaceTile`, `CloneTile`, `DeleteTile` |
| Shells | `OpenShell`, `ShellSessionAlive` — a PTY both ways |
| Events | `Subscribe` |

No request carries a descent path. The server derives location from rows it
owns. Sessions and networks never cross the wire.

Wire-only fields are derived in exactly one place each. `Tile.reference`
(this tile is a link) comes from `server.qualifyTiles`. `Tile.serves_page`,
`text_presentation`, and `status_detail` come from the plugin's `Entry`
through `pluginhost/adapter.go`. `Grid.writable`, `scratch_grid_id`, and
`menu_entries` come from the router's `GetGrid` (or `TransitQualifyGrid` for
transit). `Grid.host_content` (these rows project host state) and
`Grid.glyph` (the grid's identity face) come from the owning plugin's
`Info` through `pluginhost/adapter.go`; they are what the client reads
instead of knowing a plugin's kind, which is why a dark plugin fails the
read rather than serving a grid with a face nobody declared. `Grid.node_ns` comes from `TransitQualifyGrid` alone. `Grid.stale`
is raised by whoever serves a remembered answer.

## The router

`internal/server/router.go` is a `namespace.Namespace` over qualified ids.
It holds no state. Two codecs stand on it and route nothing themselves:
`connect_codec.go` for the browser and `namespace.Server` (`nodeexport.go`)
for other nodes. They cannot drift because they are the same value.

Two listeners. The web door (`web.bind`) serves Connect, the content door,
and the shell door behind a password cookie; serve mints the 0600
`web-password` file and prints it, delete it to rotate. The connection door
is a 0600 unix socket, never TCP; ssh forwards it between nodes. Its
`server.yaml` key is `federation:` and its file is `federation.sock` — the
word's two survivors, kept because a home already has them written down.

## The node

`server.yaml` names the node: `id`, `web`, `federation`, `connections`,
`plugins`. A missing file is a fresh home. The node builds its own home and
transport. `plugins:` lists content plugins only.

A pre-one-node home converts itself at the first load, both halves: the old
config shape — `node_id:`, every namespace under `plugins:`, `name:`, the
retired per-row flag — becomes the one-node shape, and the `db/<id>/` layout
beside it folds into `gridwell.db` (`internal/config/legacy.go` then
`node.Convert`). The node's id is the old `kind: home` row's, never
`node_id`; the originals are set aside as `server.yaml.pre-one-node` and
`db.pre-one-node`, never deleted; a conversion that cannot be made without
guessing refuses and leaves the file as it was. Serve mints what is absent.
Those are the only two config writes.

The fold is crash-safe, because it runs once, on real data, under a kill it
does not control (the desktop wrapper SIGTERMs a sidecar that has not
announced itself). It is built into `gridwell.db.converting` and published
with one rename, so a kill leaves either the untouched `db/` to retry from or
a complete `gridwell.db`; the single window in between — a finished store
beside a `db/` not yet set aside — is finished by `ensureStore`, never
converted a second time.

**Home** (`internal/local`) owns the user's content: text, urls, wells, pane
tiles, shells, and the event stream. Shells are tmux sessions on a private
socket, so they survive restarts.

**Plugins** speak `plugin.v1`. They live in their own repository,
`github.com/josephburnett/gridwell-plugins` — the shipped fs, proc and gitlab
plugins and anyone else's alike, on the same footing. Each runs as a
supervised subprocess (`internal/plugin`, the one owner of whether a plugin
is alive): one that dies is respawned with a backoff, the down and up reach
the strip as that namespace's health event, and while it is down its calls
fail honestly — nothing answers for it. A plugin holds no node fact. It answers in its own stable string keys and never sees ids, layout, or
a database. It does get a private directory, `<home>/plugins/<id>`, named to
it as `state_dir` at spawn: its own memory of its source, under cache.db's
contract — disposable, safe to delete, rewarmed by use, and never deleted by
the node. The node mints ids against those keys and
keeps the arrangement as a namespace of its own store
(`internal/pluginhost/adapter.go`). The host never imports a plugin and never
switches on its kind; every plugin behavior rides a wire declaration.
`docs/plugin-authoring.md` is the contract from the plugin's side.

**Connections** (`internal/connection`) are config rows: an immutable name, a
label, how to dial. The transport dials each at boot, lands on the far node's
home, and remembers nothing else but retired names (a name never returns).
A connection is a row in the + menu and, when dragged, an ordinary link
tile. `Server.resolve` peels the node id, the transport peels the connection
name, and the same transit rule applies at both hops.

The host-local half of a row is checked before the node serves: a missing
`addr`, a `key` or `known_hosts` path that is not there or not readable fails
`serve`, naming the connection and the exact path. Those are facts this
machine can settle, and a connection that could never dial is a
misconfiguration, not a row that comes up dark. Whether the far node answers
is not asked — offline boot is decided behavior, so an unreachable remote
stays a runtime state, with its reason on its menu row and the cache
answering for it.

**Cache** (`internal/sourcecache`) is the one read-through cache, in front
of the one seam that crosses a network — the transport — in one disposable
file (`cache.db`). A cache earns its keep across a network and nowhere
else: home is the durable store, and a plugin is a subprocess on this
machine, so both are read live. A remembered grid serves first — unstamped
inside the freshness window, stamped `stale` past it, and stamped inside it
too once the connection is known dark, with one background revalidation
kicked — so a remote round trip never sits on the read path. Darkness is
learned from any pass-through call that fails transport-shaped and from the
connection's own health on the stream the layer relays, cleared by the next
answer; the discovery announces the grid at hand, and the client re-reads
what it is showing when a source's health changes either way. A
revalidation that finds drift emits a `GridChanged` on the layer's own
event stream, served alongside the connection's own, and the client's
refetch serves the correction; a verdict evicts the remembered grid so it
surfaces on the next read. Other reads pass through and remember, and on a
transport-class failure serve the remembered answer stamped `stale`. Writes
always pass through, and their responses fold into the remembered rows.
Prefetch is a per-seam option only the transport takes, rooted at the
connections the fronted namespace's own handshake declares. A dark node is
answered by the cache. A dark *source* (the plugin answers, its directory
or API does not) is answered by the durable rows, so a move made during the
outage still lands; a dark *plugin* has nothing to answer from and fails
honestly.

### The store

One database, `gridwell.db`. It holds node facts only: the ids the node
minted (`ns`, `key` → id), layout, framing, the user's bytes, connections,
tombstones. What a connection last answered lives in `cache.db`. The
format contract is `internal/local/store/CLAUDE.md`.

**Framing** is one shape: a float center in the grid's own coordinates plus
a pane-size-independent zoom (the intrinsic ratio, live/overtake). It lives
on the row that owns the doorway — `tiles.view_cx/cy/zoom` for a well,
`grids.root_cx/cy/zoom` for a root with no doorway. Zoom 0 means never
visited. One store writer, one wire verb, one client function.

**`version`** means the user's content bytes changed: a text body, a typed
url, a typed name. It is the optimistic-concurrency claim for those three
writes and nothing else. `claimContentVersion` + `finishContentEdit` claim
and bump; `loadForWrite` + `emitTileChanged` do neither. Captures (title,
preview, url trail, shell title), framing, and layout ride the second pair.
The request fields that used to carry a claim are `reserved`, so a stale
claim cannot be expressed. `version_rule_test.go` is the table.

**Identity.** Ids are AUTOINCREMENT and never reused. Clone is an eager deep
copy: new ids, blobs shared by content address and refcount, no structural
sharing. Blobs are immutable, sha256-addressed, refcounted, and carry their
own `media_type`.

## The client

`client/*` packages are pure Go with unit tests: `pane`, `cache`, `outbox`,
`zoomtrans`, `gesture`, `wsbar`, `markdown`, `menu`, `shellstream`,
`shellwire`, `clientsync`, `errsurface`, `deadref`. `client/wasm` is the shim: canvas,
DOM, and the RPC calls. `make check` compiles the shim but executes none of
it; only the e2e gates see it. A decision that lives only in the shim is a
defect — extract it.

**Place.** A pane's place is one stack of frames (`client/pane/place.go`). A
frame is the grid you are in, the tile you came through, and your viewport
there. Every descent pushes a frame; every ascent pops one. The viewport you
left a level at is the frame you left. The URL (`url.go`) and the pane-tile
layout blob (`wire.go`) encode the stack; the bar's crumbs (`chain.go`)
project it. A pane tile swaps the whole pane tree instead — that is the one
second axis (`levels.go`), and it is session-only.

There is one `descend` and one `ascend` (`client/wasm/nav.go`). Which frame
a descent pushes is the tile's own declaration: a well or link pushes a grid
frame, a text/url/shell/page tile pushes a content frame, a pane tile
descends the window a level. Every ascent hop writes framing back through
`SetFraming` and freezes a live preview, with no claim and no version bump.
A debounced settle persister does the same without waiting for an ascent.

**Content.** A cache entry ({bytes, base version, dirty}, keyed by tile id)
owns a text tile's body. Keystrokes mirror into it; every flush goes through
`text_flush.go` by tile id, never through the DOM. A stale save 409s and
reconciles visibly. `cache.Apply` drops events older than the cached row and
spares a dirty body.

**Outbox.** `client/outbox` is the ordered record of writes the server has
not answered: framing, captures, layout, unsaved bytes. One reconcile rule
(`Record`: a transport failure parks a retry, any verdict acks). Two drains:
the retry kick on reconnect and the unload flush by `sendBeacon`. It holds
order and retry, never a copy of a value. `client/wasm/mutate.go` has two
paths: `postWriteContent` (the one write that claims a version) and
`write`/`do` (everything else).

**Dead links.** A link stores a qualified id into another namespace. When the
node stops declaring that namespace — a plugin dropped from `server.yaml`, a
connection name retired, which is forever — the link is dead: `client/deadref`
reads the handshake roster the + menu is built from, asks the router's own peel
(`rpc.OwnerNamespaceOf`) which namespace the id names, and answers from the
node's declaration rather than from a failed fetch. A dead link is drawn grey
and inert, is never fetched for, raises no notice, and does not descend; it can
still be selected, read, and deleted. Dead is not dark: a declared plugin that
is down and a declared connection that will not answer are health and
staleness, and a chain through a declared connection is the far node's to
judge, so it is never judged here.

**Events** flow only into the cache. Framing writes live only in gesture and
transition code. An event landing mid-animation updates data and redraws;
it cannot move the viewport.

**Rendered text** is a sanitized HTML overlay (`markdown.RenderHTML`:
goldmark, go-org, bluemonday). Task-list checkboxes are the one interactive
control; a click flips the source marker through the normal edit path. Every
other view paints soft-wrapped source on canvas at the same columns as the
textarea, so nothing reflows on focus. Previews render at constant scale from
the tile's own facts alone.

**The bar.** One bar at the bottom of the window (`client/wsbar`,
`bottombar.go`), always there, riding the focused pane: one crumb per frame,
the title, and the circle slot (+ menu / back / refresh). `wsbar.Rect` owns
where it is — the chrome spans the focused pane and slides under it as focus
moves, so the slot is never a wide screen away from the pane you are working
in, inside the full-width row `wsbar.Band` reserves once, whatever has focus.
That row is reserved layout — the pane tree ends at its top edge — so no pane,
and no surface sized from a pane, can occlude the bar; the plain background
either side of the bar covers no pane, and a click there does nothing. Clicks
act in the focused pane; a click in an unfocused pane moves focus, nothing
else. A crumb click is the ascent gesture; middle-click is the in-pane
shortcut.

**Shells** attach over a WebSocket at `/shell` on the page's own origin
(`client/shellwire` writes the address and frames; `client/shellstream` owns
the lifecycle). Binary frames are PTY bytes, text frames are JSON control
(resize up, one exit verdict down). The door (`internal/server/shell_door.go`)
resolves the id before accepting, so a refused upgrade never leaves a tmux
session behind. One live surface per content tile: opening it elsewhere takes
over (`pane.TakeOver`).

## The desktop

`apps/desktop/src/main` does one thing: it places native `WebContentsView`s
over the canvas for live url tiles. `WebviewRegistry` (`webviews.ts`) owns
one entry per pane. `syncURLViews` runs every frame: the renderer sends CSS-px
bounds, `roundBounds` snaps to DIP, `boundsEqual` skips churn, and
`liveOverlaysHidden` parks views off-screen while a drag, resize, or menu
needs the canvas. The focus guard keeps OS keyboard focus in the focused
pane's view only. One Chromium partition (`persist:gridwell`) holds every
live url tile, local or mounted; live tiles browse from the host's network.
Nothing here touches shells. Nothing here is visible to `make check`.

## One fact, one owner

Every fact is derived in one place and read everywhere else. The shapes to
copy:

| Fact | Owner |
|---|---|
| what a record is | `api/gridwell/v1/data.proto` → generated `api/rpc` |
| how a row is stored | `store/columns.go` |
| this tile is a link | `server.qualifyTiles` → `Tile.reference` |
| may this write claim a version | `claimContentVersion` vs `loadForWrite` |
| does this write bump `version` | `finishContentEdit` vs `emitTileChanged` |
| where is this pane | `pane.Stack` |
| is this write still owed | `outbox.Record` |
| the bytes, their version, edited or not | one cache entry per tile id |
| does this descent go live | `shellconn.DecideAutoLive` |
| is the menu open, on which pane | `client/menu` |
| the viewport transform | `zoomtrans.LiveFromIntrinsic` / `IntrinsicFromLive` |
| the shell door's address and frames | `client/shellwire` |
| what error is this | `gwerr.ClassifyError` |
| which namespace an id names | `rpc.OwnerNamespaceOf` |
| who owns this qualified id | `Server.resolve` + `server.router` |
| is this link dead | `deadref.DeadTile` over the handshake roster |
| this event stream is established | `namespace.Follow` |

Remaining seams with more than one writer, ranked by risk:

1. Native view bounds vs. canvas pane rect — reconciled every frame in
   `syncURLViews`; the math is extracted to `viewutil.ts`.
2. The drag threshold — `dragThreshold` in Go plus forced copies in
   `viewutil.ts` and the sandboxed `urlview-preload.ts`; drift-linted.
3. The `SetTile` kind→operation mapping — the proto, the local switch, and
   the hand-written per-kind builders in `api/rpc/conv.go`.
4. Text scroll of a rendered descent — the canvas wheel handler and the
   overlay's scroll listener both write `TextScrollY`.

Note: the persisted layout blob (`api/panelayout`) still spells a content
frame `text_focus`. The bytes are frozen; read it as "the content tile this
pane is inside" and do not spread the word.

## Invariants

| Invariant | Enforced by |
|---|---|
| Ids never reused | AUTOINCREMENT |
| `version` = content bytes; framing, captures, layout carry no claim | the two store pairs; reserved request fields; `version_rule_test.go` |
| Clone is an eager deep copy | `CloneTile` |
| Blobs immutable, content-addressed, refcounted | the blob layer |
| "Is a link" is one derived fact | `qualifyTiles` |
| Two wire surfaces cannot drift | both are codecs over one `namespace.Namespace` |
| An answer is never mutated under another reader | qualification clones; `TestTwoSubscribersEachSeeExactlyOnePrefix` |
| preview = descent target = ascent return | one place stack + the tile row; `framing-roundtrip.spec.ts` (the preview bytes have no oracle yet) |
| Text preview never re-wraps | `PreviewWindowFrame` takes only the tile's facts |
| Focus steal is impossible | the registry's focus guard; `control-focus.spec.ts` |
| Menu changes only by user action | `client/menu` |
| Reading never mutates | events flow only into `cache` — by inspection, no injection test yet |
| User state survives an unreachable source | the rows the user TOUCHED answer (an untouched entry has no row and is absent until the source speaks); fs/proc sweep rules |
| A workspace restores exactly as left | the live tree is the owner; the blob is derived and hash-diffed |

## Module boundaries

```
internal, server   → api
apps/gridwell      → server, api              (no plugin modules)
api                → nothing of ours
```

There is no arrow to a plugin, in either direction: plugins are a separate
repository whose modules depend on `api` and never on this one, and no
package here — test files included — may import one or name that repository
in a go.mod. `test/boundary` enforces both, along with the api module's
dependency budget, and `make check` builds every module standalone without
go.work.

A plugin loads exactly one way — `compose.LoadPlugin` spawns its binary —
which `internal/plugin`'s subprocess tests, the seam tests that spawn through
`internal/plugintest`, and `make check-connections` exercise for real. The
binaries themselves come from the sibling checkout: `make build` compiles them
out of `$(PLUGINS_DIR)` (default `../gridwell-plugins`) into this repo root,
where the loader looks.
