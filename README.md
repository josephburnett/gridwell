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
here; delete is real. Dashed ⇒ it's a link; delete only unlinks. Gray ⇒
ephemeral: a scratch shell or url visit (click a + swatch instead of
dragging it) that is deleted when you leave.

## Navigation

Everything is done with the mouse. Going **in** is a click; the way **back
out** is the trail you left behind.

The bottom bar of the focused pane shows where you are: a row of square
tile previews, one per level you descended through — a cookie-crumb trail.
Click any crumb to ascend back to that level. Middle-click anywhere in a
pane does one step. The centered title names the room you're in: left-click
it to zoom the pane tmux-style, right-click to rename.

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

## Plugins and federation

Every space is a plugin: a separate binary with its own SQLite database and
its own id space. `localdb` holds your content. `fs` and `proc` project the
filesystem and process table in as read-only grids — honest views of a
world Gridwell doesn't own (files and processes come and go, but their
placement stays stable while they exist). `ssh` mounts other machines.

You boot into home — your first plugin's root grid. The other plugins live
on the + menu's top row: click one to step through, or drag it out to drop
a doorway tile wherever you want one. Stepping into a mounted machine is
the exact same gesture as stepping into a local grid, however many hops
away it is: ids qualify and chain (`<plugin>/<id>`, `<ssh>/<plugin>/<id>`),
so a reference resolves through any number of mounts.

Two consequences of the principle at this scale:

- **Deletes never propagate across a boundary.** What you can reach through
  a link, you can unlink; the content stays where it lives.
- **Your session is yours everywhere.** Every live web tile — local or on
  the far side of a mount — browses on your machine's one Chromium session,
  from your machine's own network. Logins are host facts, not plugin facts.

One gRPC service (`api/gridwell/v1/data.proto`) is the whole interface —
client to server, server to plugin, node to node. And the storage format is
frozen and additive-only: the data is meant to outlast the application.

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

then run `gridwell serve`. In a browser, live url tiles stay frozen.
Everything else — grids, text, wells, live shells, navigation — works,
touch included.

The browser UI can require a password — add one to `server.yaml`:

```yaml
password: "something long"
```

A browser gets a login page once, then holds a cookie that never expires —
but the cookie is checked against the *current* password, so changing the
password signs every browser out. The desktop app never prompts: it
authenticates its own window automatically.

Shell tiles can be switched off node-wide:

```yaml
disable_shells: true
```

The + menu stops offering the shell primitive, and the server refuses
shell creation and every PTY attach — whichever plugin, local or mounted,
would serve it. Existing shell tiles keep their frozen previews (placement
is sacred); they just can never attach a terminal here.

Note: the password gates only the web UI. **The gRPC node export on the
same port (federation, shell transport) is unauthenticated**, as is
everything when no password is set. Bind loopback or a VPN-only address
(Tailscale is the intended transport), never an open interface. `gridwell
serve` warns loudly when the bind is not loopback.

To mount remote nodes, the remote just runs `gridwell serve` on loopback.
On the mounting machine, register the ssh plugin once:

```sh
gridwell init --kind ssh --name connections
```

The plugin has no grid of its own. Drag it from the + menu onto any grid
and descend the dropped well: a picker lists your named connections —
select one to wire the well to it (the same connection, never a copy), or
fill in host and user (port, key path, known_hosts default to the usual
places) to create a new one. Clicking the plugin in the menu opens the
same picker and descends straight into the connection you pick; the picker
is also where connections are renamed and deleted (deleting one tombstones
its namespace forever). Connections are data — add a machine without
touching config or restarting. Keys stay files on your machine; an
unverified host is refused, and key material never rides tile content.

The CLI is three commands: `gridwell init` (register a plugin), `gridwell
serve` (run the node), and `gridwell backup` (snapshot every plugin DB,
safe while serving).

## Reading further

- **`ARCHITECTURE.md`** — how the machine works: layers, the wire contract,
  the invariants and where each is enforced.
- **`CLAUDE.md`** — the engineering charter: how to change this code
  without breaking the principle.
- **`apps/desktop/README.md`** — building, testing, and packaging the
  desktop shell.
