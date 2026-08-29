# Gridwell

Gridwell is a personal operating environment built on a single principle:
**things stay as you leave them.**

Tiles live on a 2D grid. Drop one at a coordinate and it stays there. It
works like a physical space: step out of a room and look back, and you see
it exactly as you left it. Step back in, and it is the same room. Most
software resorts, refreshes, and relayouts itself underneath you. Gridwell
doesn't. You rearrange it constantly — drop tiles, capture pages, type —
but nothing changes except by your explicit action. You navigate by
remembering where things are, not by searching for where they went.

Four faces of the one rule:

1. **Placement is persistent.** A tile at (x, y, w, h) stays until you move
   it. No auto-relayout, no resort, ever.
2. **Identity is persistent.** A tile's id is assigned once and never
   changes. Editing rewrites the tile in place, so a bookmark or deep link
   always resolves to *that* tile. Copies exist only by the explicit clone
   gesture — an eager, independent deep copy, so editing one can never
   touch another.
3. **Preview = descent target = ascent return.** A tile's preview shows
   what you'll see when you go in. Coming out writes back what you were
   just looking at. One stored framing, read the same way every time.
4. **Reading never mutates.** Panning, zooming, and scrolling re-frame a
   preview (a real, persisted change), but a tile's version bumps only
   when you change what it *says* — never how it is framed.

When a design decision is unclear, the principle decides — over
performance, over elegance, over convenience. If a design lets something
change that you didn't change, the design is wrong.

## The primitives

Five tile kinds, created from the **+ menu** in the bottom bar:

- **text** — a Markdown document. Descend and type; toggle a rendered view.
- **url** — a web page. Frozen, it's a captured preview; descend and it
  reopens live (a real embedded Chromium view, desktop app only).
- **shell** — a real PTY (bash under tmux). Descend and it reconnects to
  the same session, scrollback and running processes intact.
- **well** — a nested grid. Its preview is the room seen through the door.
- **pane** — a workspace: a whole split-pane arrangement made durable as a
  tile. Step in and the window becomes that arrangement. Step out and it is
  saved exactly as arranged.

Borders carry meaning, everywhere, no exceptions. Solid ⇒ the content lives
here. Dashed ⇒ it's a link; delete only unlinks. Gray ⇒ ephemeral: a
scratch shell or url visit (click a + swatch instead of dragging it) that
is deleted when you leave.

On the local plugin, delete is a safety net: the tile moves into the
**trashcan** (its swatch sits in the + menu beside the plugin), filed
under the current month. Same tile, same id — links keep resolving, and
dragging it back out restores it. Deleting something already inside the
trash is real and forever. Ephemerals and other plugins' projections skip
the net.

## Navigation

Everything is done with the mouse. Going **in** is a click; the way **back
out** is the trail you left behind.

Every pane's bottom bar shows where that pane is: a row of square tile
previews, one per level you descended through — a cookie-crumb trail.
Click any crumb to ascend back to that level (in the focused pane;
clicking an unfocused pane's bar just focuses it). Middle-click anywhere
in a pane does one step. The centered title names the room you're in:
left-click it to zoom the pane tmux-style, right-click to rename.

| Gesture | Action |
|---|---|
| left-click a tile (in a focused pane) | **descend** into it |
| left-click a bar crumb | **ascend** to that level |
| middle-click | ascend one step |
| scroll wheel | zoom, centered on the cursor — over a well it steers *into* the room |
| scroll wheel over the bar | zoom the pane as if from its center (the escape hatch when wells tile the view wall-to-wall) |
| left-drag empty space | pan |
| left-drag a tile | move it |
| drag a tile onto the + button (it becomes a trashcan) | delete |
| right-drag from a tile's center | **clone** |
| right-drag from a tile's edge | resize |
| right-drag inward from a pane border | split the pane — the side follows your drag |
| left-drag a divider | resize panes; drag on through and panes crush closed, turning red before they go |

Dragging across a plugin boundary follows the identity rule: a left-drag
creates a **link** (the ghost turns dashed with a chain badge to say so),
a right-drag creates a **copy**. There is no cross-boundary move — moving
is the explicit two-step, copy then delete, so an identity break only
happens where you can see it.

Touch maps onto the same vocabulary: tap = click, long-press = right
button, two-finger tap = ascend, pinch = zoom.

The URL bar is a *place*. It always reflects the focused pane — plugin,
path of tile ids, viewport — so any moment is a bookmarkable deep link.

Note: the split-pane layout at the window root is deliberately
session-ephemeral — scaffolding, not content. The durable home for an
arrangement you care about is the pane tile.

## One node, plugins, connections

A Gridwell node is one server with one config, one id and one database.
The node **is** its home: `~/.gridwell/gridwell.db` holds your content
(text, urls, shells, wells, pane tiles) under the node's id. `fs`,
`proc` and `gitlab` are **content plugins** — separate binaries that
answer in their own stable keys (a path, a pid, a todo) while the node
mints the ids and keeps the arrangement in that same database — honest
views of a world Gridwell doesn't own (files and processes come and go,
but their placement stays stable while they exist). **Connections** are
other nodes, declared in config and dialed at boot.

You boot into home. Plugins and connections live on the + menu's top
row: click one to step through, or drag it out to drop a doorway tile
wherever you want one. Stepping into another machine is the exact same
gesture as stepping into a local grid, however many hops away it is: ids
qualify and chain (`<id>/<tile>`, `<plugin>/<tile>`,
`<id>/<connection>/<remote-id>/<tile>`), so a reference resolves through
any number of mounts.

Two consequences of the principle at this scale:

- **Deletes never propagate across a boundary.** What you can reach through
  a link, you can unlink; the content stays where it lives.
- **Your session is yours everywhere.** Every live web tile — local or on
  the far side of a mount — browses on your machine's one Chromium session,
  from your machine's own network. Logins are host facts, not plugin facts.

Plugins can also serve their content **as web content**: the server's
`/content/` door turns a GET into a plugin RPC and streams the answer back
as an ordinary web page. The fs plugin uses it for the files a browser
presents natively — descend into an image and you see the image (live in
the desktop app, opened in a new tab from a plain browser), an HTML file
serves as a real page with its relative images and styles, and everything
still federates: a mounted machine's photos view fine from here. Served
pages run sandboxed (an opaque origin — no cookies, no reach into your
Gridwell), and the door is gated by a token derived from your password.

One gRPC service (`api/gridwell/v1/data.proto`) is the whole interface —
client to server, server to plugin, node to node. And the storage format is
frozen and additive-only: the data is meant to outlast the application.

## Running it

The full experience is the Electron desktop app (live url tiles need it):

```sh
make vendor   # once, online: deps + caches
make launch   # build and run (the first run mints ~/.gridwell/server.yaml)
```

The same server also serves plain browsers — one instance, one origin, for
the desktop window and your phone. Everything the node is lives in
`~/.gridwell/server.yaml` (a missing file is a fresh home — the first
`gridwell serve` mints the id and writes it):

```yaml
id: 1b467bbd65466256f8a64c538cabdac8   # the node = its home; minted, immutable
web:                       # browsers + the desktop window
  bind: "100.64.0.7:8080"  # your Tailscale IP
federation:                # other nodes mounting this one
  socket: ~/.gridwell/federation.sock   # a 0600 unix socket — never TCP
connections:               # other nodes
  - name: geneva           # immutable — a namespace segment
    label: Geneva
    host: geneva.example
    user: joe
    addr: /home/joe/.gridwell/federation.sock   # the REMOTE's socket
plugins:                   # content plugins ONLY
  - kind: fs               # id minted by serve
    label: Home dir
    config: { root: /home/joe }
```

Give the web door a reachable address, then run `gridwell serve`. In a browser, live url tiles stay frozen.
Everything else — grids, text, wells, live shells, navigation — works,
touch included.

The browser UI always requires a password. It is not in the yaml: serve
mints a random one into `~/.gridwell/web-password` (0600) and prints it
at startup — carry it to a browser once. A browser gets a login page once, then holds a cookie that never expires
(re-issued on every request) — but the cookie is checked against the
*current* password, so deleting `web-password` (a new one is minted on
the next start) signs every browser out. The desktop app never prompts:
it authenticates its own window automatically.

Shell tiles can be switched off node-wide:

```yaml
disable_shells: true
```

The + menu stops offering the shell primitive, and the server refuses
shell creation and every PTY attach — whichever plugin, local or mounted,
would serve it. Existing shell tiles keep their frozen previews (placement
is sacred); they just can never attach a terminal here.

Note: the password gates the web door. The gRPC node export (federation,
the desktop's shell transport) is unauthenticated and therefore **only
exists as a unix socket**, mode 0600 — the kernel admits your uid and
nobody else; there is no TCP form, so no config can expose it. ssh is
the one authenticated transport between nodes. Bind the web door to
loopback or a VPN-only address (Tailscale is the intended transport),
never an open interface. (A retired key — `node_id`, a flat `bind:`, a
yaml `password:`, a `kind: home` plugin entry — fails loudly with the fix,
never loads silently.)

To mount remote nodes, the remote just runs `gridwell serve`; its
`federation.socket` path (`<home>/federation.sock` by default) is what
the connection's `addr` names — required, no default — reached through
ssh's unix-socket forwarding.

A connection reaches the other node one of two ways, chosen by what you
fill in: an SSH host + user bridges over ssh (the authenticated
transport), or an address alone connects DIRECTLY — another gridwell on
this machine, or across the tailnet, where the network is the trust
boundary.

Connections are server config (v2, #269): declare each one in
server.yaml's `connections:` list — a `name` (immutable: it is the
namespace segment inside every reference through it), an optional
`label`, and either `host`/`user`/`key` for the ssh bridge or `addr`
alone for a direct dial. Every connection then sits in the + menu as its
own row: click descends straight into that machine, drag drops a link
well. The node dials them all at boot and logs each verdict, so the
terminal says exactly which connections are live. Removing one from the
yaml retires its name forever (`retired_names` is the graveyard). Keys
stay files on your machine; an unverified host is refused, and key
material never rides tile content.

The CLI is four commands: `gridwell serve` (run the node — a missing
server.yaml is a fresh home, its id minted on the spot; a pre-one-node
home converts itself at first serve), `gridwell status` (is this home
being served?), `gridwell backup` (snapshot `gridwell.db` + server.yaml,
safe while serving), and `gridwell clear-browser-data` (drop the desktop
app's Chromium session).

## Reading further

- **`ARCHITECTURE.md`** — how the machine works: layers, the wire contract,
  the invariants and where each is enforced.
- **`CLAUDE.md`** — the engineering charter: how to change this code
  without breaking the principle.
- **`apps/desktop/README.md`** — building, testing, and packaging the
  desktop shell.
