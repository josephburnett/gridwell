# Gridwell

Gridwell is a personal operating environment. Tiles live on a 2D grid. Drop a tile at a coordinate and it stays there. The preview of a tile is what you see when you descend into it. Ascend after editing, and the preview shows what you were just looking at.

## The guiding rule

Things stay where you put them.

That is the rule. It decomposes into four invariants:

1. **Placement is persistent.** A tile at (x, y, w, h) in a grid stays there until something explicitly moves it. No automatic relayout. No spontaneous resort.

2. **Identity is persistent across clones.** Two clones of a tile are the *same* tile until one of them is edited. They share an object identity. The integer row ID can change under copy-on-write; the object identity does not.

3. **Preview = descent target = ascent return.** What the parent grid shows for a well is what you see when you descend into it. Edit, ascend, and the preview reflects exactly what you were looking at. The well's stored framing *is* the preview.

4. **Mutation is local and reflected.** Every mutation goes through the store. The store fans the change to every open view.

These are not four rules. They are one rule from four angles.

## The six primitives

Gridwell has six tile kinds:

- **`text`** — markdown content. Editable. Green outline.
- **`url`** — a URL. Live browser tab on descent, frozen JPEG in preview. Purple outline.
- **`well`** — a tile pointing at another grid. Descend to enter the child canvas. Blue outline.
- **`blackhole`** — drops what you drop on it. Deletion is a place. Red outline.
- **`file-well`** — a view of a directory on the host filesystem. Red outline.
- **`process-well`** — a view of host processes. Red outline.

Six primitives. Move, clone, resize, and descend over these six cover everything Gridwell does.

## The color grammar

The outline tells you whether the principle holds inside.

- **Blue** — grid well. Inside is Gridwell.
- **Red** — exit well. The contents come from outside Gridwell: a directory (file-well), the process table (process-well), or the bit bucket (blackhole). Descent paths still resolve, and gestures still mean the same thing. Contents reflect a world Gridwell does not own.
- **Brown** — root grid. You cannot ascend further.
- **Green / purple** — content tile kind. Markdown vs URL.

## Identity is `(object_id, version)`

Every tile and every grid has two identities:

- **`object_id`** — a UUID. Survives copy-on-write and cloning. Two clones share an `object_id`.
- **`version`** — an integer. Bumps on every content mutation. Two clones share a version until one of them is edited.

The composite `(object_id, version)` is the unit of horizontal navigation: all instances of this tile, at this version. It is also the concurrency primitive. A mutation commits only if its claimed version matches the version on disk. Optimistic concurrency, same shape as Git.

## Copy-on-write is how cloning stays cheap

Cloning a well does not copy its child grid. The clone points at the same grid and bumps its refcount. The first mutation through the clone's path forks the spine of shared grids up to the highest one still uniquely owned, then writes into the fork.

That is how cloning a thousand-tile well stays cheap, and how two clones diverge without leaking writes into each other. Same pattern as Git, applied to grids instead of trees.

COW is the implementation strategy. The principle it serves is placement persistence — your edit goes where you made it, and nowhere else.

## Panes are views, not state

A pane is a window into the persistent thing. Panes split tmux-style: split, swap, resize, close. The split tree lives in the browser session and is not persisted.

The persistent thing is the canvas. The pane tree is how you happen to be looking at it right now. Reload throws away the layout because the layout was never part of where you put things.

The URL captures the focused pane's path and viewport so a single link can describe a place. It does not capture the split tree.

## The mutation surface

Mouse-only. No modifiers.

- **Descend** — left-click a well or content tile.
- **Ascend** — left-click the pane's edge band.
- **Move** — left-click + drag a tile.
- **Clone** — right-click + drag from a tile's center.
- **Resize** — right-click + drag a tile's outer ring.
- **Delete** — drag a tile onto a blackhole.
- **Create** — drag a tile out of the palette.
- **Pan** — left-click + drag empty space.
- **Zoom** — wheel.
- **Scroll** (inside a text tile) — wheel.
- **Split / swap / resize / close pane** — right-click + drag in the pane regions.

Text tiles take keyboard input because text is being edited. URL tiles forward keystrokes to the embedded page. The canvas itself reads no modifier keys.

## Discoverability and cancellation

Every gesture has a visible preview before it commits. A right-click + drag shows what the action will be — move, clone, resize, split, swap, close — and where it will land, before you release. You can cancel by dragging back to a neutral zone. Esc is a fallback, not the cancel mechanism.

Right-click with the mouse stationary reveals what is available. The user should never need to remember a gesture.

## Preview invariants, precisely

Three rendering paths have to agree for preview = descent = ascent to feel true. They agree because they all read the same stored state.

**Grid well.** The well row stores `view_x`, `view_y`, `view_zoom`: a rectangle in the child grid's coordinates. The parent shows that rectangle as the well's preview. Descent zooms continuously into that exact rectangle — no jump, no resize. Ascent writes the current pane viewport back into the well row. One value, three jobs: preview frame, descent target, ascent return.

**Text tile.** The tile row stores the document-space window it was last framed at (scroll offset + window size) and its mode, rendered or text. The preview crops the re-rendered document to that window. Descent shows the same crop. Editing scrolls within it. Ascent writes the current window back to the tile.

**URL tile.** The preview is a JPEG captured when the previous descent closed. Descent opens a fresh tab and navigates to the stored URL. The page may have changed since you left. The preview is what was last frozen, not what is currently live. This is where principle #3 meets its limit: URLs point at a world Gridwell does not own.

**File-well / process-well.** The preview is a deterministic render of the well's contents at last close. Descent shows the current contents of the directory or process table. The view is stable in shape: same sort, same projection, same gestures. Contents may have changed underneath.

## What's a principle, what's an implementation detail

Do not change these without a real conversation:

- Things stay where you put them.
- Six primitives.
- Color grammar: blue, red, brown, green, purple.
- Preview = descent = ascent.
- Identity is `(object_id, version)`.
- Mouse-only, no modifiers.
- Every gesture has a preview and a cancel zone.
- Panes are views, not state.

Change freely if they get in the way:

- COW grids with refcounts.
- SQLite as the store.
- WebSocket for URL streaming, SSE for events.
- `Path` on mutation RPCs (needed for COW spine).
- Chromium for URL tiles.
- The browser + WASM as the UI.

## Non-requirements

- Multi-user. Single-tenant by design. No auth.
- Persisted pane layout.
- A keyboard shortcut layer over the canvas.
- Undo/redo today. History falls out of `version`; replaying it is future work.

## Horizontal navigation across clones (future work)

`version` is in the schema now to enable it later.

A tile cloned twice and then edited gives three rows with the same `object_id`: the original at version *N*, clone A at version *N+k*, clone B at version *N+m*. Horizontal navigation lets you jump between them at the same version, or step through the version history of any one of them.

Today: the schema carries `(object_id, version)` and writes them correctly. Tomorrow: expose them as a gesture.
