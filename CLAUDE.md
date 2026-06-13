# Gridwell

Gridwell is a single-tenant personal operating environment. Tiles live on a 2D grid; drop one at a coordinate and it stays there. A tile's preview is what you see when you descend into it; ascend after editing and the preview shows what you were just looking at.

This file is the contract. The **Invariants** and **Rules for changes** sections are binding — don't violate them without an explicit conversation with the user.

## The guiding rule: things stay where you put them

Four faces of one rule:

1. **Placement is persistent.** A tile at (x, y, w, h) stays until something explicitly moves it. No auto-relayout, no resort.
2. **Identity is persistent across clones.** Two clones are the *same* tile — shared `object_id` and `version` — until one is edited. The integer row id can change under copy-on-write; the `object_id` cannot.
3. **Preview = descent target = ascent return.** A well's stored framing *is* its preview; descending restores it; ascending writes it back.
4. **Mutation is local and reflected.** Every change goes through the store, which fans an event to every open view.

## The seven primitives

Seven tile kinds; move / clone / resize / descend over them cover everything.

| kind | what | outline |
|---|---|---|
| `text` | editable markdown | green |
| `url` | a URL; frozen JPEG preview, refresh floats a live Electron WebContentsView over the pane | purple |
| `well` | points at a child Gridwell grid; descend to enter | blue |
| `blackhole` | deletion sink — drop a tile on it to delete | red |
| `file-well` | a view of a host directory | red |
| `process-well` | a view of host processes | red |
| `shell` | bash in a gridwell-private tmux session; frozen JPEG, refresh attaches a live PTY | red |

## Color grammar

**Outline = what's inside:**
- **Blue** — grid well; inside is Gridwell.
- **Red** — exit well; contents come from outside Gridwell (a directory, the process table, the bit bucket, a bash session). Descent and gestures still work; the world beyond isn't owned by Gridwell.
- **Brown** — root grid; can't ascend further.
- **Green / purple** — content tile (markdown / URL).

**Line style = owned vs link:**
- **Solid** — the real thing. Delete (drag onto a blackhole, either mouse button) acts for real: a host file/dir goes to the **system trash** (recoverable, never `rm -rf`); a process gets `SIGTERM`.
- **Dashed** — a link out of Gridwell (file-well, process-well, or a file dragged into a regular grid). Delete only *unlinks* (drops the tile row); the real file/process is untouched. `DeleteTile` routes on the parent grid's `source_kind`.

## Identity, version, copy-on-write

- `object_id` (UUID) survives COW and cloning; clones share it.
- `version` (int) bumps on every **content** mutation. Clones share a version until one is edited — that's the unit of horizontal navigation and the optimistic-concurrency key (a mutation commits only if its claimed version matches disk).
- **Framing is not a content edit.** Panning / zooming / scrolling (the `view_*` / `text_*` window) re-frames the preview *without* bumping version. It still forks a shared grid for write isolation, so the framing lands in this clone's row only.
- **COW:** cloning a well shares its child grid (refcount++). The first mutation through a clone's path forks the shared spine up to the highest uniquely-owned grid, then writes into the fork. Cheap clones, no write leakage between them.

## Preview = descent = ascent

All three read the same stored state, so they agree:
- **Well** — row stores `view_x/view_y/view_zoom` (a rect in child coords): preview frame, descent target, ascent return — one value, three jobs.
- **Text** — row stores the doc-space window (scroll offset + size) and mode (rendered/text). The preview crops the re-rendered doc to that window.
- **URL** — row carries a frozen JPEG + URL string. Descent shows the preview (no network). Refresh floats a native Electron WebContentsView; a capture pump mirrors its live frames into the frozen preview other panes render. Ascent freezes via `SetURLState` (a *versioned* edit → forks in a cloned grid, never leaks). The live view eats all mouse input over its rect, so ascent/refresh route through the pane's corner circle, not the content box.
- **File/process-well** — preview is a deterministic render at last close; descent shows current host contents (which may have changed underneath).

## The mutation surface — mouse-only, no modifiers

- **Descend** — left-click a well / content tile. **Ascend** — middle-click a pane, or right-click its corner circle (drag out to cancel).
- **Move** — left-drag a tile. **Clone** — right-drag from a tile's center. **Resize** — right-drag a tile's outer ring.
- **Delete** — drag a tile onto a blackhole (left or right drag).
- **Create** — drag from the palette. **Pan** — left-drag empty space. **Zoom / text-scroll** — wheel.
- **Refresh URL/shell** — right-drag down inside the descent (goes live if frozen, reloads if already live).
- **Split / swap / resize / close pane** — right-drag in the pane regions; a new split auto-ascends one level. Left-drag a pane boundary resizes but never closes (live tiles stay put).

Every gesture shows a live preview of the action and where it lands before release, plus a neutral cancel zone (Esc is only a fallback). Right-click held stationary reveals what's available — the user should never have to remember a gesture. Text tiles take keyboard input (editing); URL tiles forward keys to the page; the canvas itself reads no modifiers.

## Panes are views, not state

A pane is a window into the canvas. The split tree lives in the browser session and is never persisted — reload discards the layout. The URL captures the focused pane's path + viewport (a link to a *place*), not the split tree.

## Architecture — where things live

- `internal/store/` — SQLite store and all invariants: `cow.go` (preWrite / forkGrid / refcounts), `tiles.go`, `move_clone.go`, `url.go`, `shell.go`, `source_*.go` (file/proc wells), `blobs.go`, `trash.go`.
- `internal/server/` — Connect-RPC handlers, the shell PTY WebSocket (`/rpc/ShellStream`), the `/preview/tile/` image endpoint, SSE events.
- `internal/rpc/` + `api/gridwell/v1/data.proto` — wire types and proto. Regenerate with `buf generate` after editing the proto.
- `internal/tmux/` — shell sessions.
- `client/wasm/` — the WASM UI: `input.go` / `right_button.go` (gestures), `render.go`, `url_stream_client.go`, `shell_stream_client.go`. `client/pane/`, `client/cache/` — pane tree + client cache.
- `apps/desktop/` — Electron shell (TypeScript): loads the renderer from the server's own origin, hosts live URL WebContentsViews and the JPEG capture pump.

These are implementation choices — swap them freely if they get in the way: COW + refcounts, SQLite, the content-addressed blob store (sha256, refcounted), tmux, Electron, WebSocket/SSE, `Path` on RPCs, browser + WASM UI.

## Rules for changes

**Invariants (binding — don't break without a conversation):** things stay where you put them; seven primitives; color grammar (blue / red / brown / green / purple); preview = descent = ascent; identity is `(object_id, version)`; mouse-only, no modifiers; every gesture has a preview and a cancel zone; panes are views, not state.

**When you touch the store:**
- Content mutations take a `Path` + `Version`: call `preWrite` (COW fork), `checkTileVersion`, then `bumpTileVersion`. Framing mutations fork but **don't** bump the version.
- Refcounting goes through the single `tileRefs` table in `cow.go`, used by fork / clone / delete / GC alike — never hand-roll a per-kind inc/dec. A new tile kind that holds a grid or blob must be added there, and `property_test.go` must generate it. The property walk + `verifyRefcounts` (which counts both `blob_id` and `preview_blob_id`) are the safety net that catches leaks.
- Host deletes go to the trash (`trash.go`), not `os.Remove`; process deletes `SIGTERM`.
- Shell sessions are keyed by **tile id**, not `object_id`: a PTY can't be forked, so a cloned shell is a screenshot with no session. The shell WebSocket is same-origin — never reintroduce `InsecureSkipVerify`.

**Testing mode — no backward compatibility yet.** The user wipes the DB on schema/label changes, so:
- **No migrations.** New schema goes straight into `Schema`; old DBs are expected to be deleted.
- **No backward-compat shims** in the server, RPC layer, or wasm client. If a field changes meaning, just change it.
- **No "tiles created before X" code paths.**

Prefer the clean break. This ends only when the user says we're exiting testing mode (then every schema change needs an idempotent migration and every wire change a compat shim).

**Non-requirements (don't build these):** multi-user / auth; persisted pane layout; a keyboard shortcut layer over the canvas; background fetches (a URL tile opens a live view only on explicit refresh); undo/redo (history falls out of `version` — future work).

## Future work — horizontal navigation across clones

A tile cloned twice then edited yields three rows sharing one `object_id` at versions *N*, *N+k*, *N+m*. The schema already carries `(object_id, version)` correctly; the remaining work is a gesture to jump between instances at the same version, or step through one instance's version history.
