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

## The seven primitives

Gridwell has seven tile kinds:

- **`text`** — markdown content. Editable. Green outline.
- **`url`** — a URL. Frozen JPEG preview by default; refresh gesture opens a live Electron view (WebContentsView) over the pane. Purple outline.
- **`well`** — a tile pointing at another grid. Descend to enter the child canvas. Blue outline.
- **`blackhole`** — drops what you drop on it. Deletion is a place. Red outline.
- **`file-well`** — a view of a directory on the host filesystem. Red outline.
- **`process-well`** — a view of host processes. Red outline.
- **`shell`** — an interactive bash session. Frozen JPEG preview by default; refresh gesture attaches a live PTY. The bash lives in a gridwell-private `tmux` session (keyed by tile id), so refresh resumes in the directory the user last `cd`-ed into for as long as that tmux server is alive. Red outline.

Seven primitives. Move, clone, resize, and descend over these seven cover everything Gridwell does.

## The color grammar

The outline tells you whether the principle holds inside.

- **Blue** — grid well. Inside is Gridwell.
- **Red** — exit well. The contents come from outside Gridwell: a directory (file-well), the process table (process-well), the bit bucket (blackhole), or an interactive bash session (shell). Descent paths still resolve, and gestures still mean the same thing. Contents reflect a world Gridwell does not own.
- **Brown** — root grid. You cannot ascend further.
- **Green / purple** — content tile kind. Markdown vs URL.

The *line style* says whether the tile is owned or a link. **Solid** — the tile is the real thing; deleting it (drag onto `/dev/null`, with either mouse button) deletes for real: a host file or directory goes to the system trash (recoverable, not `rm -rf`), a process gets `SIGTERM`. **Dashed** — the tile is a link to outside Gridwell: a file-well, process-well, or a file dragged out of a source well into a regular grid. Deleting a dashed tile only *unlinks* it (drops the tile row); the real file/process is untouched. The same entity is solid inside its source well and dashed once dragged into a regular grid — `DeleteTile` routes on the parent grid's `source_kind`.

## Identity is `(object_id, version)`

Every tile and every grid has two identities:

- **`object_id`** — a UUID. Survives copy-on-write and cloning. Two clones share an `object_id`.
- **`version`** — an integer. Bumps on every content mutation. Two clones share a version until one of them is edited. Framing is *not* a content edit: panning, zooming, or scrolling a well or text tile (its `view_*` / `text_*` window) re-frames the preview but does not bump the version, so clones stay at a shared version even after you navigate one of them differently. It still forks a shared grid for write isolation — your framing lands in your clone's row only — it just doesn't count as an edit.

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
- **Ascend** — middle-click anywhere in a pane, or right-click the pane's corner circle (drag out of the circle to cancel). The colored edge band no longer ascends — it's a thin 1px frame now.
- **Move** — left-click + drag a tile.
- **Clone** — right-click + drag from a tile's center.
- **Resize** — right-click + drag a tile's outer ring.
- **Delete** — drag a tile onto a blackhole.
- **Create** — drag a tile out of the palette.
- **Pan** — left-click + drag empty space.
- **Resize pane (no close)** — left-click + drag a pane boundary. Clamps to a recoverable minimum; never closes a pane (so live tiles stay put).
- **Zoom** — wheel.
- **Scroll** (inside a text tile) — wheel.
- **Refresh URL** — right-click + drag down inside a URL descent. Goes live if frozen; reloads if already live.
- **Split / swap / resize / close pane** — right-click + drag in the pane regions. A newly split pane auto-ascends one level so it isn't a duplicate view.

Text tiles take keyboard input because text is being edited. URL tiles forward keystrokes to the embedded page. The canvas itself reads no modifier keys.

## Discoverability and cancellation

Every gesture has a visible preview before it commits. A right-click + drag shows what the action will be — move, clone, resize, split, swap, close — and where it will land, before you release. You can cancel by dragging back to a neutral zone. Esc is a fallback, not the cancel mechanism.

Right-click with the mouse stationary reveals what is available. The user should never need to remember a gesture.

## Preview invariants, precisely

Three rendering paths have to agree for preview = descent = ascent to feel true. They agree because they all read the same stored state.

**Grid well.** The well row stores `view_x`, `view_y`, `view_zoom`: a rectangle in the child grid's coordinates. The parent shows that rectangle as the well's preview. Descent zooms continuously into that exact rectangle — no jump, no resize. Ascent writes the current pane viewport back into the well row. One value, three jobs: preview frame, descent target, ascent return.

**Text tile.** The tile row stores the document-space window it was last framed at (scroll offset + window size) and its mode, rendered or text. The preview crops the re-rendered document to that window. Descent shows the same crop. Editing scrolls within it. Ascent writes the current window back to the tile.

**URL tile.** The tile carries a frozen JPEG preview and a URL string. Descent shows the preview — no fresh view, no network. The refresh gesture asks the Electron shell to float a native WebContentsView over the pane's content box, navigating to the stored URL. A capture pump mirrors the live view's frames into the frozen preview that every other view of this tile renders, so navigation in one pane is visible in another in real time. Ascent freezes through `SetURLState`: the latest URL and JPEG persist (as a versioned content edit, so a URL tile in a cloned grid forks rather than leaking into its clones) and the live view tears down. The frozen preview fills its pane in cover mode and the overflow dimension scrolls with click + drag. The live view eats all mouse input over its rect, which is why ascent/refresh route through the pane's corner circle rather than the content box. Border tint distinguishes live from frozen.

**File-well / process-well.** The preview is a deterministic render of the well's contents at last close. Descent shows the current contents of the directory or process table. The view is stable in shape: same sort, same projection, same gestures. Contents may have changed underneath.

## What's a principle, what's an implementation detail

Do not change these without a real conversation:

- Things stay where you put them.
- Seven primitives.
- Color grammar: blue, red, brown, green, purple.
- Preview = descent = ascent.
- Identity is `(object_id, version)`.
- Mouse-only, no modifiers.
- Every gesture has a preview and a cancel zone.
- Panes are views, not state.

Change freely if they get in the way:

- COW grids with refcounts.
- SQLite as the store.
- A content-addressed blob store (sha256, refcounted) backing text content and the URL/shell preview JPEGs. `forkGrid`, `CloneTile`, and the delete paths all route refcount inc/dec through one table keyed on tile kind, so a new kind that holds a grid or blob can't silently leak.
- `tmux` for shell PTYs: each shell tile gets a gridwell-private tmux session keyed by *tile id* (not `object_id`). A PTY can't be forked, so a cloned shell is a screenshot with no session; the original keeps the session. cwd persists only as long as that tmux server is alive.
- An Electron shell hosting live URL tiles as native WebContentsViews, with a JPEG capture pump feeding the frozen previews. The live view captures all mouse input over its rect (hence the corner-circle gutter for ascent/refresh).
- WebSocket for the shell PTY stream (same-origin enforced), SSE for store events.
- `Path` on mutation RPCs (needed for COW spine).
- The browser + WASM as the UI.

## Non-requirements

- Multi-user. Single-tenant by design. No auth.
- Persisted pane layout.
- A keyboard shortcut layer over the canvas.
- Background fetches. The URL tile only opens a live view when the user explicitly refreshes.
- Undo/redo today. History falls out of `version`; replaying it is future work.

## Testing mode (no backward compatibility yet)

Gridwell is in pre-release testing. The user will blow away the database when schema or label-derivation changes need it, so:

- **Do not write migrations** for the existing DB. Don't add `addColumnIfMissing` calls, data-rewrite UPDATEs, or "if old shape then upgrade" branches. New schema goes straight into `Schema`; existing DBs are expected to be deleted.
- **Do not write backward-compatibility shims** in the server, the RPC layer, or the wasm client. If a field changes meaning, just change it.
- **Do not preserve old code paths** for "tiles created before X". The DB starts fresh after every behavior change that needs it.

This ends when the user explicitly says we're exiting testing mode. After that, every schema change needs an idempotent migration and every wire-format change needs a compat shim. Until then, prefer the clean break.

## Horizontal navigation across clones (future work)

`version` is in the schema now to enable it later.

A tile cloned twice and then edited gives three rows with the same `object_id`: the original at version *N*, clone A at version *N+k*, clone B at version *N+m*. Horizontal navigation lets you jump between them at the same version, or step through the version history of any one of them.

Today: the schema carries `(object_id, version)` and writes them correctly. Tomorrow: expose them as a gesture.
