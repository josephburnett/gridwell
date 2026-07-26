# Gridwell

Gridwell is a personal operating environment built on a single principle:
**things stay as you leave them.**

Tiles live on a 2D grid. Drop one at a coordinate and it stays there. It
works like a physical space: step out of a room and look back (ascent) and
you see it exactly as you left it. Step back in (descent) and it is indeed
the same. Most software resorts, refreshes, and relayouts itself underneath
you. Gridwell doesn't. It is write-heavy — you rearrange it constantly, drop
tiles, capture pages, type — but nothing changes except by your explicit
action. You navigate by remembering where things are, not by searching for
where they went.

Everything else in this document derives from that one principle.

## The principle, derived

Four faces of the one rule:

1. **Placement is persistent.** A tile at (x, y, w, h) stays until you move
   it. No auto-relayout, no resort, ever.
2. **Identity is persistent.** A tile's id is assigned once and never
   changes. Editing rewrites the tile in place, so a reference — an embed, a
   bookmark, a deep link — always resolves to *that* tile. Copies exist only
   by the explicit clone gesture: an eager, independent deep copy. Editing
   one copy can never touch another.
3. **Preview = descent target = ascent return.** A tile's preview shows what
   you'll see when you descend into it. Ascending writes back what you were
   just looking at. One stored framing, read the same way every time, so the
   round trip is idempotent.
4. **Reading never mutates.** Looking at something is never a content edit.
   Panning, zooming, and scrolling re-frame a preview (a real, persisted
   change), but a tile's `version` bumps only when you change what it says —
   never how it is framed.

When a design decision is unclear, the principle decides — over performance,
over elegance, over convenience. If a design lets something change that you
didn't change, the design is wrong.

## The primitives

Five tile kinds, created by dragging a swatch out of the **+ palette**:

- **text** — a Markdown document. Descend and type, rendered or raw.
- **url** — a web page. Frozen, it's a captured preview; live, it's a real
  embedded Chromium view (desktop app only).
- **shell** — a real PTY (bash under tmux), live in the tile.
- **well** — a nested grid. Its preview is the room seen through the door.
- **pane** — a workspace: a whole split-pane arrangement made durable as a
  tile. Step in and the window becomes that arrangement. Step out and it is
  saved exactly as arranged.

Borders carry meaning. Solid ⇒ delete is real. Dashed ⇒ it's a link, and
delete only unlinks. Everywhere, no exceptions. Gray ⇒ ephemeral: a scratch
shell or url visit (click a palette swatch instead of dragging it) that is
deleted when you ascend out of it.

Text tiles can **embed** other tiles. Drag any tile onto an open text
document and a Markdown link to its id is inserted — a soft link, like a
symlink. Rendered, it paints a live preview of the target; clicking it
descends into the real thing. And because the link is an id and ids never
move, it survives any restructuring of the space around it.

## Navigation

Everything is done with just the mouse: ascent, descent, zoom, scroll,
resize, clone, move.

| Gesture | Action |
|---|---|
| left-click | focus the pane; a click on a tile in a focused pane **descends** into it |
| middle-click | **ascend** |
| right-click the corner circle | ascend (the discoverable form) |
| scroll wheel | zoom, centered on the cursor — the point under it stays put |
| left-drag empty space | pan |
| left-drag a tile | move it (never across a plugin boundary) |
| drag a tile onto the + button (it becomes a trashcan) | delete |
| right-drag from a tile's center | **clone** — the one gesture that crosses boundaries |
| right-drag from a tile's edge | resize |
| right-drag inward from a pane edge | split the pane (the new pane is another view of where you are) |
| drag a divider | resize panes; right-drag can crush a pane closed |
| left-click the name pill | rename the room you're in |
| right-click the name pill | zoom the pane, tmux-style |

The corner circle also does the kind-specific thing: go-live on a frozen url
tile, back on a live one, refresh on a shell. Touch maps onto the same
vocabulary: tap = click, long-press = right button, two-finger tap = ascend,
pinch = zoom.

The URL bar is a *place*. It always reflects the focused pane — anchor, path
of tile ids, viewport — so any moment is a bookmarkable deep link. Inside a
workspace, the bar at the bottom of the window names the nesting: left-click
a crumb to rename it, right-click to leave it.

Note: the split-pane layout at the window root is deliberately
session-ephemeral — scaffolding, not content. The durable home for an
arrangement you care about is the pane tile.

## Plugins and federation

Every space is a plugin: a separately compiled binary with its own SQLite
database and its own id space. `localdb` holds your content. `fs` and `proc`
project the filesystem and process table in as read-only grids — honest
views of a world Gridwell doesn't own (files and processes come and go, but
their *placement* stays stable while they exist). `ssh` mounts another
machine.

The landing page is the **node grid**: one link tile per plugin. Descending
into a link tile is a portal, and it's the same portal whether the link
points at a local plugin, a mounted directory, or a machine on the other
side of an SSH tunnel. Drop a connection to another Gridwell node and go
into it — the exact same experience as your local database. Ids qualify and
chain (`<plugin>/<id>`, `<ssh>/<plugin>/<id>`), so a reference resolves
through any number of hops.

The principle has two consequences at this scale:

- **Cross-plugin left-drag is a link, right-drag is a copy.** Identity never
  migrates between id spaces, and deletes never propagate across a plugin
  boundary.
- **Your session is yours everywhere.** Every live web tile — local or on
  the far side of a mount — browses on your machine's one Chromium session,
  from your machine's own network. Sessions and networks are host facts,
  not plugin facts.

One gRPC service (`api/gridwell/v1/data.proto`) is the whole interface —
client to server, server to plugin, node to node. Every byte crosses it,
including live shell PTYs. And the storage format is frozen and
additive-only: the data is meant to last forever, even as the application
changes.

## Running it

The full experience is the Electron desktop app (live url tiles need it):

```sh
make vendor   # once, online: deps + caches
make init     # once: register a localdb plugin in ~/.gridwell
make launch   # build and run
```

The same server also serves plain browsers — one instance, one origin, for
the desktop window and your phone. Give it a reachable address in
`~/.gridwell/server.yaml`:

```yaml
bind: "100.64.0.7:8080"   # your Tailscale IP
```

then run `gridwell serve` (or launch the app; its window uses the same
origin). In a browser, live url tiles stay frozen. Everything else — grids,
text, wells, navigation, live shells — works, touch included.

Note: **the API is unauthenticated.** Anyone who can reach the bound address
can read and write every tile and open shells on your machine. Bind loopback
or a VPN-only address (Tailscale is the intended transport), never an open
interface. `gridwell serve` warns loudly when the bind is not loopback.

To mount a remote node, the remote just runs `gridwell serve` on loopback —
the same one port that serves browsers answers the tunnel, so nothing new is
exposed. On the mounting machine:

```sh
gridwell init --kind ssh --name work \
  --config host=myserver.example:22 \
  --config user=joe \
  --config key=/home/joe/.ssh/id_ed25519 \
  --config known_hosts=/home/joe/.ssh/known_hosts \
  --config addr=127.0.0.1:8080
```

`known_hosts` is required — the plugin refuses to trust an unverified host.

The CLI is three commands: `gridwell init` (register a plugin), `gridwell
serve` (run the node), and `gridwell backup` (snapshot every plugin DB, safe
while serving).

## Reading further

- **`CLAUDE.md`** — the philosophy in full and the engineering charter: how
  to change this code without breaking the principle.
- **`ARCHITECTURE.md`** — the machine: layers, seams, invariants, and where
  the bugs come from.
- **`apps/desktop/README.md`** — building, testing, and packaging the
  desktop shell.
