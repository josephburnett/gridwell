# Gridwell

Gridwell is a single-tenant personal operating environment. Tiles live
on a 2D grid; drop one at a coordinate and it stays there. A tile's
preview is what you see when you descend into it; ascend after editing
and the preview shows what you were just looking at.

## The guiding rule: things stay as you left them

**This is the deciding factor.** When a technical decision is unclear,
the option that preserves this principle wins — over performance, over
elegance, over implementation convenience. If a design lets something
change that the user didn't change, the design is wrong.

Gridwell is a physical space. You rearrange it constantly — it is
write-heavy and mutates freely (drop a tile, pan, capture a page,
type) — but **nothing changes except by your explicit action.** Step
out of a room and look back (ascent): you see it exactly as you left
it. Step back in (descent): it is exactly the same. The round trip is
idempotent, and that holds for *everything* — content, view framing,
and layout alike. Reading never mutates.

Four faces of one rule:

1. **Placement is persistent.** A tile at (x, y, w, h) stays until
   something explicitly moves it. No auto-relayout, no resort.
2. **Identity is persistent and stable.** A tile's row id is a
   permanent handle: editing a tile never moves it (mutation is
   in-place; the id never changes), so a reference always returns
   *that* tile. Copies are made only by the explicit **clone** gesture
3. **Preview = descent target = ascent return.** A well's stored
   framing *is* its preview; descending restores it; ascending writes
   it back. One value, read the same way every time.
4. **Mutation is local and reflected.** Every change goes through the
   store, which fans an event to every open view.

## Why clone is eager (the deciding case)

When a design choice is in question, run it through one test: **after this
change, does everything the user didn't touch stay byte-for-byte the same,
and does every reference still resolve to the thing it named?**

The clone question settled this permanently. A copy-on-write design shares
a subtree on clone and forks it on the first edit. A fork *re-rows* tiles —
the edited instance's id moves and a sibling inherits the old id. A stored
reference (an embed link, a saved URL, a bookmark) can then silently resolve
to the *wrong instance's* content. No patch recovers it: object-ids can't
distinguish clones (clones share them), and capturing the full id-path only
narrows the failure, because a fork reassigns a whole grid's ids at once.

The option that keeps the principle: **never reassign ids.** Editing is
in-place (the id never moves); copying happens only at the explicit clone
gesture, as an eager, independent deep copy. A tile's id is then a permanent,
durable reference — an embed, a deep-link URL, a bookmark *is* that id, and
it always resolves to the same tile.

The cost lands where the user asked for it: clone is O(rows) of cheap metadata
(≈ a few ms for ~1000 tiles; blobs are shared by content address, so no content
is copied), paid exactly at the "make me a copy" gesture. Edits stay O(1).

## Identity and clone semantics

Nothing the user didn't touch ever re-rows. The consequences:

- **A tile's id is its durable identity.** Assigned once by its owning plugin,
  never changed; editing a tile rewrites it in place. An embed, a deep-link URL,
  or a bookmark *is* that id — it always resolves to the same tile.
- **`version` (int)** bumps on every content mutation: the optimistic-concurrency
  key and the spine of edit history.
- **Framing is not a content edit.** Panning / zooming / scrolling re-frames the
  preview without bumping version. It is still a real, persisted, in-place
  mutation — it just isn't a content change.
- **Clone is an eager copy.** Deep copy of the tile and, for a well, its whole
  child subtree within the *same plugin*: new ids for the copy, blobs shared by
  content address. No structural sharing, no copy-on-write — so an edit to one
  copy can never touch another, and no id is ever reassigned.
- **Cross-plugin clone is a link, not a copy.** Cloning a well across plugin
  boundaries creates a tile in the destination plugin whose child points back at
  the source plugin's grid (a qualified cross-plugin reference). The source
  grid is shared, not duplicated. Cloning a leaf (text/url/shell) across
  plugins copies the bytes into the destination plugin's blob store.
- A clone may carry the source's `object_id` as a **provenance marker** (these
  came from the same origin) — used only by future horizontal-navigation
  gestures, never for identity or routing. The plugin-scoped numeric id is identity.

## Plugin namespaces and stable identity

Gridwell is a federated space. Each **plugin instance** — a local Gridwell DB,
a mounted filesystem, the process table, a remote server reached over SSH —
owns its own id space and is identified by a UUID assigned once and stored
permanently (in the plugin's own DB and referenced in `server.yaml`).

A globally qualified id is `<plugin_uuid>/<local_integer>`. The local integer
is allocated by the plugin (SQLite AUTOINCREMENT; never reused once issued).
The plugin UUID makes it globally unique. Both are necessary in every stored
cross-plugin reference.

**Every plugin has its own SQLite DB.** This is not optional — it is how the
plugin allocates stable numeric ids and stores tile positions durably. A
filesystem plugin maps paths → integers; a proc plugin maps PIDs → integers;
a localdb plugin *is* its DB. There is no separate ephemeral cache: plugin
storage is the authoritative state for the plugin's tiles.

**The SSH gateway is transparent.** When an SSH connection is established the
local server calls `Info()` on the remote and adds its plugin UUIDs to the
local routing table. Remote plugin ids appear directly in paths —
`local_main_uuid/12/remote_main_uuid/45/remote_fs_uuid/7` — so a URL from the
remote server works as-is locally when the connection is active.

**URL and path format.** The descent path (the stack of tiles you descended
through) is encoded as alternating plugin-uuid / tile-id segments, with the
plugin uuid omitted when it is the same as the previous segment:

```
/<p1>/<t1>/<t2>/<p2>/<t3>     ← tiles t1+t2 in plugin p1, then t3 in plugin p2
```

Two kinds of links:
- **URL bar / bookmarks** — the full descent path + viewport. A *place*: where
  you are, which wells you descended through, what viewport is framed. Must be
  the full path; the COW spine and ascent return target depend on it.
- **Markdown embed links** — a single qualified id (`<plugin_uuid>/<tile_id>`).
  A *thing*: globally routable, stable, survives restructuring of the containing
  space. The same id works from any Gridwell client that has the plugin in its
  routing table. The server resolves it directly without needing the descent path.

## The plugin gRPC interface (same as the server interface)

Every Gridwell node — a local server, a plugin, a remote server reached over
SSH — implements the same single gRPC service. The client calls the local server;
the local server calls plugins; an SSH gateway proxies to a remote server. Same
wire protocol at every hop.

The service extends the existing Gridwell API with lifecycle and session methods:
`Info`, `Attach`/`Detach` (replaces `Bootstrap`; takes a config map, returns root
grid id + capabilities + network context), `Probe` (confirmed presence), `GetSession`/
`PutSession` (per-plugin Chromium session blob), `OpenShell` (replaces the WebSocket
`/rpc/ShellStream`). The `CreateFileWell` and `CreateProcessWell` RPCs are removed;
a well whose child lives in a different plugin is just a `well` tile with a
cross-plugin `child_grid_id`.

## Making changes

No part of Gridwell may be constructed without tests. If you find a bug,
you must answer the question "why was this not caught by a test?". If
you fix a bug, you must write a test that reproduces it. Even if it means
refactoring to make the code testable. There are exceptions to this
rule, small and trivial layers of glue code. But these must be minimized.
I would prefer to slow down and fix test gaps over getting feature
finished.

The codebase must remain DRY. A little extra time to look around and
see if this is a pattern elsewhere will help keep entropy at bay. I
would rather amortize this cost across each commit that have to keep
coming back and asking for the code to be cleaned up. Because I don't
want to have to read the code. I want the code to be written cleanly,
cleary, and accurately. I was it to be simple (not complected) and
to accomplish this is must be DRY.

When you need to make judgement calls about what the right behavior
should be, the primary rule of Gridwell decides. As I move around the
space, nothing should change that I did not change. Previews should
show was I was looking at when inside a tile. Wells should show what
I was looking at when I was inside. Moving between panes should not
cause their contents to change. There are exceptions to this, such as
the menu which actually appears only in the pane currently in focus.
This is by design to show focus. And processes and files outside
Gridwell may come and go outside our control. But things like their
placement, may remain stable as long as they are present.
