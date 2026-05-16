# Gridwell — Implementation Specification

A spatial information system. The user arranges tiles on an infinite 2D grid. Some tiles are "wells" — they contain another grid (the "third dimension"). Each user has their own tree.

**Gridwell**'s three primitives:

  - **grid** — the infinite plane
  - **tile** — a movable, resizable, non-overlapping element on a grid (file or well)
  - **well** — a tile that contains another grid

A **pane** is a non-overlapping view on a grid (tmux-style) that allows multi-tasking and teleportation of tiles between grids.

The central gesture is **ascent** — popping out of a well into its parent grid. Ascent is also the operation that creates new structure: popping out at the root creates a new root above.

This document specifies the system to be built. Read it end-to-end before starting.

---

## 1. Core Principle

**Things stay where you put them. The world is only mutable where you can see it.**

Every design decision must serve this. If a feature would silently shift things outside the user's view, it is wrong.

---

## 2. Architecture

- **Server**: single Go binary. Subcommands: `serve`, `init`, `adduser`. Persists to one SQLite file. Serves the web app and a JSON-over-HTTP RPC API.
- **Client**: written in Go, compiled to WebAssembly. Loaded by a small static `index.html`. The client renders to a single full-window `<canvas>`. No HTML/CSS UI components beyond the canvas, the login form, and the file upload `<input>`.
- **Transport**: JSON over HTTP/1.1. Each RPC method is a `POST` to `/rpc/<MethodName>`; `Subscribe` is a `GET` server-sent-events stream at `/rpc/Subscribe`. Wire types are hand-coded in `internal/rpc/types.go`; no protobuf code generation.
- **Persistence**: SQLite via `modernc.org/sqlite` (pure-Go, no cgo) so the binary is statically linkable. WAL mode. All writes wrapped in transactions.
- **Dependency policy**: Go modules from the standard ecosystem are permitted, no cgo. The pure-Go SQLite driver is a Go module vendored at build time; it imposes no system dependencies. **Chromium is an optional runtime dependency** required only for the `text/uri-list` (URL) tile type. When Chromium is present, the server manages one headless Chromium process per logged-in user and drives it via the Chrome DevTools Protocol; when Chromium is absent, all other tile types still work, and existing URL tiles still render their last captured preview but cannot be woken or created (see §8.3).
- **Auth**: cookie-based session. Cookie is `HttpOnly`, `Secure` (in production), `SameSite=Strict`. Session token is a random 32-byte value, stored hex-encoded.
- **Testing**: every package has unit tests. Server logic tested at the service-method layer with an in-memory SQLite. WASM client logic factored so non-DOM code is testable under standard `go test`.

Project layout:

```
/cmd/gridwell/               main.go — subcommand dispatch
/internal/cli/               init / adduser / serve subcommand implementations
/internal/server/            HTTP server, RPC handlers, session middleware
/internal/store/             SQLite schema + queries; CoW logic
/internal/auth/              password hashing (argon2id), session token generation
/internal/rpc/               hand-coded wire types (JSON over HTTP)
/client/                     Go-WASM client; pure logic in subpackages
/client/wasm/main.go         WASM entry point
/web/                        index.html, the .wasm output, wasm_exec.js
```

---

## 3. Core Concepts

### 3.1 Grid

A grid is an infinite 2D plane of integer cells. Cells are either empty or covered by exactly one tile (well or file). A grid has an owner, a group, and a unix-style mode.

The grid's own "where" lives nowhere — a grid is not placed at a coordinate. It is reached through a well that points to it.

### 3.2 Tile (Well or File)

A tile is a rectangle on a grid. It has:

- `(x, y)` — top-left cell of its footprint.
- `(w, h)` — width and height in cells. Default `1×1`. Must be positive.
- `(view_x, view_y)` — internal viewport offset (see §3.5).
- `owner`, `group`, `mode`.

Two tiles in the same grid never overlap. Drops, moves, resizes, and clones that would cause overlap are refused.

Tiles are typed: `well` or `file`.

### 3.3 Well

A well is a tile whose content is another grid (its **child grid**). Pointer is well → child grid (parent-to-child). A well also has:

- `child_grid_id`
- `capped` — boolean. A capped well cannot be descended into.

### 3.4 File

A file is a leaf tile. It has:

- `mime_type`
- `blob_id` — content-addressed storage (sha256). Multiple file tiles may reference the same blob.

### 3.5 Internal Viewport

Both wells and files have `(view_x, view_y, view_zoom)` describing what region of their interior is currently framed by their footprint, and at what intrinsic zoom ratio.

- For a well, `(view_x, view_y)` is in cells: the top-left of the visible region of the child grid centered on the well's footprint. `view_zoom` is a window-independent intrinsic ratio (live child zoom divided by the well's overtake zoom at the time of ascent), which the client uses to reconstruct the preview cell size in the parent grid and to land at the user's prior zoom on re-descent. A `view_zoom` of `0` is the sentinel "never visited"; the client substitutes a default ratio in that case.
- For a file, `(view_x, view_y)` is in pixels relative to the file's full rendered content. `view_zoom` is similarly an intrinsic FileZoom-to-overtake ratio used to restore the file's zoom on re-descent and to scale its parent-grid preview.

**Resize exposes/hides content; it does not scale.** Making a tile larger reveals more of its interior at the same scale; making it smaller hides some.

The internal viewport is persistent state on the tile. Changing it is a write (`SetTileViewport`). Snapshots taken on ascent reflect the current viewport.

### 3.6 Tree

Wells form a strict tree:

- Each grid contains zero or more well-tiles.
- Each well-tile points to exactly one child grid.
- A grid is referenced by at most one well at any time **in any single user's tree** (clones are version-distinct rows; see §3.8).
- No grid points "up." Ascent is path-dependent and tracked per-pane.

Each user has a `root_grid_id`. Ascending from this grid creates a new grid that contains a single well pointing to the old root. The new grid becomes the user's `root_grid_id`. There is no global root.

### 3.7 Cap and Fill

- **Cap** (write perm on parent grid): set `capped = true` on a well. The well remains visible in its parent grid but cannot be descended into. Any panes currently inside the capped well or any of its descendants are forced to ascend back to the capped well's parent. Capped wells render distinctly from open wells (e.g., dark cover) and distinctly from unreadable wells (§7.4).
- **Redig** (write perm): clear `capped`. Contents are restored exactly as they were.
- **Fill empty well** (write perm): destroy the well and its (empty) child grid. **This is the only irreversible operation.** A well is "empty" iff its child grid contains zero tiles. Filling a non-empty well is refused; cap it instead.

### 3.8 Identity: object_id and row_id

Every grid and every tile has two identifiers:

- `id` (row id): the SQLite primary key. Unique per row.
- `object_id` (text, UUID): stable across clones. A clone of a well produces a new tile row (new `id`, same `object_id`).

Filtering by `object_id` shows the lineage of clones of one logical thing. There is no implicit history beyond this — within a single clone lineage, edits update rows in place.

### 3.9 Cloning and Copy-on-Write

Cloning a well produces a new well tile with a new `id` but the same `object_id` and the same `child_grid_id` as the original. The two clones share the child grid until something inside is written through one of them.

CoW forks at the **grid boundary**:

1. A write happens to a tile `N` whose containing grid `G` is referenced by more than one well (refcount > 1).
2. Before applying the write, the system creates a new grid `G'` (new `id`, same `object_id`, same owner/group/mode), copies all tile rows from `G` into `G'` (each new row gets a new `id`, same `object_id`), and updates the well *along the editing pane's descent path* to point to `G'` instead of `G`.
3. The write is then applied against `G'`.

Blobs are content-addressed and never forked.

Forking propagates upward only as far as needed: the rewritten well lives in some grid, and if that grid is also shared, it must fork too — and so on until an unshared grid is reached, or until the well is in the user's root grid.

Reference counts on grids are maintained as wells are created, cloned, deleted, and moved.

**Cloning a tile requires write permission on the source tile** (not just read). This prevents unauthorized indefinite preservation of read-only content.

---

## 4. Pane Model

The viewport UI is a tmux-style pane tree.

- The window is split by a binary tree of tiles. Internal tiles are horizontal or vertical splits with a ratio in `[0, 1]`. Leaves are panes.
- At any moment there is at least one pane. The last pane cannot be closed.
- Each pane has its own state: descent path (list of `(well_row_id)` from root to the currently-viewed grid), viewport center `(cx, cy)` in cells (floating point), and zoom level (floating point, continuous). When a pane is "inside" a markdown file, the pane additionally holds `FileFocus` (the file tile's row id), `FileMode` (`"text"` or `"rendered"`), `FileScrollX/Y`, and `FileZoom`; the pane's `Path` remains the parent grid, so file-focus is logically a leaf state that lives on top of the parent grid.
- **Focus** is a single pane, determined by the most recent mousedown. There is no separate keyboard-vs-mouse focus split: input is mouse-only (§10.3).
- Splitting a pane clones the current pane's state into both halves. After the split, the panes evolve independently.
- Closing a pane discards its state.
- Pane-tree state is *ephemeral*: it lives only in the client and is **not** persisted to `localStorage`. On reload the layout resets to a single pane; the focused pane's descent path and viewport are recovered from the URL (§10.4). Within-session state that survives across descent/ascent but not reload — the per-pane saved-state stack, the per-file "last mode" memo — is held in memory only.

### 4.1 Operations on Panes

All pane operations are driven by the **right mouse button**, with the gesture chosen by the cursor's starting region inside the pane (see §10.3):

- `Split horizontal` / `Split vertical`: right-press in the outer ring near an edge, drag, release. Creates a sibling pane on that edge with the new pane inheriting the originating pane's state (path / viewport / zoom / file-mode).
- `Swap panes`: right-press in the inner third of a pane, drag to another pane, release. The two panes' positions in the tree are exchanged.
- `Resize split`: right-press in the resize band along the shared edge between two panes and drag. Squeezing one side past a minimum threshold closes that pane.
- There is no keyboard shortcut for any of these.

### 4.2 Cross-Pane Drag (Teleport)

To move a tile from pane A's grid to pane B's grid:

1. Both panes must be open and pointing at the relevant grids (achieved via split + walk).
2. The user left-drags the tile from A. While dragging, a "ghost" follows the cursor across pane boundaries. The ghost smoothly interpolates its cell size between the source and destination scales so dropping into a smaller (well-preview) cell looks continuous.
3. On drop in pane B at coordinates `(x, y)`, the system attempts the move. If `(x, y, w, h)` doesn't fit (overlap with another tile), the drop is refused and the ghost snaps back.

Cloning shares the gesture with moving; the clone-vs-move distinction is **not yet wired to a modifier in the client** — every left-drag commits as a move (`MoveTile`). The clone branch in the drop handler exists in the code and is exercised by `CloneTile` RPCs from other entry points; a modifier binding is a small future addition.

If the cursor is over an open well during a drag, the drop target promotes from the parent grid to the well's child grid (visible through the well's preview); this is the "drop into a well" gesture. Conversely, mousedown on a tile visible inside another well's preview begins a "pull out of well" drag with that child tile as the source.

There are no bookmarks, persistent portals, or cross-tree navigation. Every navigation has a real cost paid in walking.

---

## 5. Navigation

- **Move = zoom.** No avatar. The viewport is "where you are." Left-drag on empty grid space pans the mouse-focused pane; mouse wheel zooms it, centered on the cursor. The viewport is `(cx, cy, zoom)` per pane.
- **Continuous zoom.** Any positive zoom value is allowed within `[zoomMin, zoomMax]` (currently `0.25` … `8.0`); inside a focused file, `FileZoom` ranges up to `fileZoomMax` (`50.0`) so a heading can be enlarged enough to fill a parent cell. Zoom is a viewport property only — it does not change cell size in storage.
- **Locality of action.** A tile may be operated on only if its footprint is currently visible in the focused pane. The client enforces this (gestures are scoped to visible tiles); the server also enforces it via the framed `view_rect` included in each mutating RPC (§6). The server's check is "the affected footprint *intersects* the view rect" — any single cell of the tile inside the rect is enough — except for `CapWell` / `RedigWell`, which require the well's footprint to be *entirely* inside the rect.
- **Descent.** Left-click on a well descends into it (the pane's descent path is appended with the well's row id) via an animated zoom-in transition. Left-click on a markdown file descends into the file: the pane's `FileFocus` is set to the file's row id while `Path` stays at the parent grid. The pane's pre-descent viewport is pushed onto an in-memory stack for the matching ascent.
- **Ascent.** Triggered by a left-click in the outer edge band of the pane (the region near the pane border, when there is somewhere to ascend to). For files this restores the parent-grid viewport; for grids it pops the last entry off the descent path. Refused if path is empty *and* the pane is not file-focused — but the gesture is still meaningful at root: `AscendAtRoot` creates a new grid, places a new well at `(0, 0)` 1×1 pointing to the old root, and updates the user's `root_grid_id`.
- **Cell size.** Cell size in storage is unitless integer coordinates. The client renders cells at `64` logical pixels at zoom `1.0` (`cellPx` constant). Implementation hint, not spec: feel free to tune.

---

## 6. RPC Service

Wire types are hand-coded in Go in `internal/rpc/types.go` as JSON-over-HTTP messages under `/rpc/<MethodName>`. All requests except `Login` carry the session cookie; the server resolves it to a `user_id` in middleware. All mutating RPCs return the canonical post-write state of the affected grid(s) so the client can reconcile.

Methods (Go method names; HTTP path is `/rpc/<MethodName>`):

  - **Auth**: `Login`, `Logout`, `Whoami`
  - **Read**: `GetGrid` (returns `Grid` + `[]Tile` + `readable` / `writable` flags), `GetBlob`, `GetTilePreview` (raw JPEG bytes for a URL tile's current preview)
  - **Mutations**: `CreateWell`, `CreateFile`, `MoveTile`, `CloneTile`, `ResizeTile`, `SetTileViewport`, `SetGridDefaultView`, `CapWell`, `RedigWell`, `FillWell`, `UpdateFileContent`, `WakeURL`, `CaptureURL`, `ForkURL`
  - **Tree-level**: `AscendAtRoot`
  - **Real-time**: `Subscribe` (server-sent events stream for grid mutations), `URLStream` (per-tile bidirectional WebSocket carrying screencast frames out and synthetic input events in; see §8.3)

All RPC methods accept `POST` requests except `Subscribe`, which is a `GET` (an EventSource fetch). Public endpoints (`Login`, `Logout`) need no session; all others require a valid session cookie resolved by middleware to a `user_id`.

Every mutating request includes:

  - `path { well_ids: [...] }` — the originating pane's descent path (sequence of well row ids from the user's root grid down to the leaf grid). The server uses this both to validate the request (each well must point at the next grid) and to walk the CoW fork up the path.
  - `view_rect { x, y, w, h }` — the framed region of the originating pane in the affected grid's own coordinates.

The server enforces locality of action by rejecting any mutation whose target footprint does not **intersect** `view_rect` (at least one cell inside is enough); `CapWell` and `RedigWell` use the stricter rule that the well's footprint must be **entirely contained** in `view_rect`. Cross-grid `MoveTile` and `CloneTile` carry a second pair (`dest_path`, `dest_view_rect`) for the destination side; both sides are checked.

### 6.1 Subscribe

The client opens one server-streaming `Subscribe` per descent-path that any of its panes is currently inside. To minimize complexity, a simpler initial implementation: **one subscription per user**, server pushes any change to any grid the user owns. The client filters and applies to whichever panes need updating.

Events:

Event kinds (`rpc.Event.Kind`):

  - `grid_changed` — a grid the user can see was modified
  - `tile_changed` — a tile was upserted (payload: `Tile`)
  - `tile_removed` — a tile was deleted (payload: `grid_id`, `tile_id`)
  - `grid_forked` — a CoW fork happened; clients update `child_grid_id` refs
  - `url_preview_updated` — a URL tile's `preview_jpeg` was overwritten (payload: `grid_id`, `tile_id`); the client invalidates its cached preview and re-fetches via `GetTilePreview` on next render

### 6.2 Concurrency Semantics

All mutations are SQLite transactions. On conflict, the rule is **snap to canonical**: the client applies mutations optimistically, and reconciles when the server's response or `Subscribe` event reports a different state. No override feedback in v1; the affected tile simply moves to its canonical position.

Drag and drop are client-side only until release. The release issues one mutation RPC. The "ghost" the user sees during drag is purely local.

### 6.3 URLStream

`URLStream` is the only transport in the system that is not JSON-over-HTTP / SSE. It is a bidirectional WebSocket at `/rpc/URLStream?tile_id=<id>` used for two things, both per-tile:

- **Server → client**: CDP screencast frames from the tile's live Chromium tab as JPEG bytes, plus URL-changed notifications when the tab navigates.
- **Client → server**: synthetic input events (`mousemove`, `mousedown`, `mouseup`, `wheel`, `keydown`, `keyup`) the server translates into CDP `Input.dispatch{Mouse,Key}Event` calls and forwards to the Chromium tab.

A URL tile can have multiple concurrent `URLStream` connections (e.g., the same tile viewed from two gridwell browser tabs). All connected clients receive the same frames; all can send input. There is no input-ownership coordination — conflicting cursors physically fight, by design.

`URLStream` open is refused if the tile is not currently live, if the tile is not a `text/uri-list` tile, or if Chromium is unavailable. Clients are expected to call `WakeURL` first if the tile is dormant.

Frames are sized to whatever viewport the server has currently set on the Chromium tab (§8.3, "Viewport"). When the client's pane resizes, the client sends a `resize` message; the server calls `Emulation.setDeviceMetricsOverride` and subsequent frames arrive at the new size.

---

## 7. Authentication and Permissions

### 7.1 Bootstrap

The first user is created via:

```
gridwell adduser <username>
```

This subcommand is allowed regardless of whether any users exist. It prompts for a password on stdin (no echo). It is also the only way to create new users in v1 — there is no signup flow in the web UI.

Logging in via the web UI is impossible until at least one user exists. The login page tells the user this.

When a user is created:

1. A group is created with the same name as the user. This is the user's **primary group**.
2. The user is added to that group.
3. An empty grid is created, owned by the user, group set to the primary group, mode `0o640`. This is the user's `root_grid_id`.

### 7.2 Sessions

`Login` accepts `username` and `password`. On success, the server creates a session row with a random 32-byte token, sets a cookie with TTL **30 days**, and returns user info. `Logout` deletes the session row and clears the cookie.

Passwords are hashed with argon2id (use `golang.org/x/crypto/argon2`). Salt per-user.

### 7.3 Permissions Model

Unix-style. Every grid and every tile has `owner`, `group`, `mode`. `mode` is the lower 9 bits of a unix mode (rwxrwxrwx, but `x` is unused — only `r` and `w` matter). Default mode for new grids and tiles is `0o640` (rw- r-- ---).

- **Read** on a grid: needed to see its tile list, descend into it.
- **Write** on a grid: needed to create/move/delete tiles in it.
- **Read** on a tile: needed to see its rendered preview (file content or well's child grid sample).
- **Write** on a tile: needed to resize, edit content, set viewport, cap/redig, **and to clone**.

Effective check: user matches `owner` → use owner bits; else user is in `group` → use group bits; else use other bits.

Cloning a tile requires `w` on the *source tile* and `w` on the *destination grid*.

Moving a tile requires `w` on both source grid and destination grid (often the same grid).

Filling an empty well requires `w` on the parent grid.

Editing file content requires `w` on the file tile.

### 7.4 No-Read and No-Write Visibility

- **No-read on a tile**: the tile is rendered as a generic "locked" tile in its parent grid (e.g., padlock icon). Visually distinct from capped wells. The user can see the tile's footprint and that it exists, but not its contents or preview. Descent is refused.
- **No-write on a tile**: the tile renders normally; mutations are refused.
- **No-read on a grid**: the parent well that points to it is rendered as a "locked well." Descent is refused.
- **No-write on a grid**: the grid renders normally; mutations are refused.

### 7.5 Per-User Trees

Each user's `root_grid_id` is theirs alone. The system does not yet expose any way to descend into another user's tree. Permission bits are wired up so that a future "share a well" feature can be added by simply granting another user read/write on a well or grid; for now, no flow creates cross-user references.

### 7.6 Clone Inherits

A clone inherits the source's `owner`, `group`, and `mode` exactly. This prevents permission laundering by cloning into a context where you'd otherwise have less control.

### 7.7 CLI Reference

```
gridwell init [--db <path>]              create the SQLite database and schema
gridwell adduser <username> [--db <path>] interactive password prompt; creates user, primary group, root grid
gridwell serve [--db <path>] [--addr :8080]
```

No CLI for group management, chmod, or chown in v1. The data model fully supports them; commands are deferred.

---

## 8. Content Types (v1)

Three MIME types in v1:

### 8.1 `text/markdown` (read/write)

- Stored as a UTF-8 blob.
- Rendered (on descent) by a built-in markdown editor inside the pane. The editor is a pure scrolling text view: vertical flow, soft-wrapped to the file's footprint width. No 2D placement inside.
- The internal `(view_x, view_y)` pair is `(0, vertical_scroll_pixels)` for markdown.
- All rendering happens client-side via the canvas `fillText` API (which reaches the browser's native text renderer through `syscall/js`). The Go-WASM client implements a small markdown layout pass: headings (H1–H3), bold, italic, inline code, code blocks, blockquotes, unordered lists, paragraphs. No tables, images, or links rendering in v1 (sources still display verbatim).
- Parent-grid preview: the same client renderer draws the markdown into a virtual layout, then crops to the file's framed window starting at `view_y` and scaled to the footprint. No server-side rendering and no cached preview blob.

### 8.2 `image/*` (read-only)

- Stored as a raw image blob.
- Supported variants: `image/png`, `image/jpeg`, `image/gif`, `image/webp`.
- On descent: the client renders the image into the pane. Pan and zoom inside.
- The internal `(view_x, view_y)` pair is the pixel offset of the framed window into the full image.
- Snapshot: simply crop the image at `(view_x, view_y, w*cell, h*cell)`.

### 8.3 `text/uri-list` (URL tile)

A URL tile renders a live web page via a server-side headless Chromium tab streamed into the descended pane. Its preview in the parent grid is a JPEG screenshot kept fresh while the tile is "live" and frozen when the tile is "dormant."

**Identity.** A URL tile is identified by a URL stored on the tile row in `url_string` (one URL only — no multi-URL `text/uri-list` in v1). The URL is set on creation and thereafter updates **silently** to reflect in-page navigation inside the tile's live Chromium tab. There is no visible address bar; navigation happens by clicking links inside the page. To use an unrelated URL, the user creates a new tile.

**Liveness.** A URL tile is in one of two runtime states:

- **Live** — the server holds a Chromium tab open at the tile's URL. The tab renders the page; CDP screencast frames are streamed to whichever clients have an open `URLStream` for the tile (see §6.2). Navigations inside the tab mutate `url_string`.
- **Dormant** — no Chromium tab exists. The tile is represented purely by its last-captured `preview_jpeg` and its stored URL.

Liveness is **runtime state**, not persistent. It is held in the server's in-memory Chromium driver, not in SQLite. On server start, all tiles begin **dormant** regardless of their state before shutdown.

A new tile is born **live**. The user toggles liveness with **right-click** on the tile (descended or not). Right-click on a live tile fires `CaptureURL` (live → dormant); right-click on a dormant tile fires `WakeURL` (dormant → live). Reload is right-click twice. Liveness is independent of descent: ascending out of a live tile does **not** capture it. The Chromium tab stays open and continues to render at a reduced preview cadence.

**Preview.**

- Stored as JPEG bytes in `tiles.preview_jpeg` (mutable BLOB column; not part of the content-addressed `blobs` table).
- While **live**, the server writes the latest screencast frame to `preview_jpeg` and emits a `Subscribe` event invalidating the cached preview for connected clients. Cadence: at the descended pane's stream rate while the tile is descended into a focused pane (typically 10–30 frames per second, bounded by CDP screencast), and approximately **every 700 ms** when no pane is descended into the tile but it is still live.
- While **dormant**, `preview_jpeg` is frozen — the bytes captured at the moment the tile went dormant.
- The parent-grid rendering shows `preview_jpeg`, cropped/scaled to the tile's footprint. A subtle live indicator (small dot or faint pulsing border) distinguishes live tiles from dormant ones.
- Clients fetch the bytes via `GetTilePreview(tile_id)`.

**Descent.**

- Into a **live** tile: the descended pane opens a `URLStream` WebSocket for the tile. Inbound JPEG frames are drawn onto a canvas filling the pane. Mouse and keyboard input from the focused pane are forwarded as synthetic CDP `Input.dispatch{Mouse,Key,Touch}Event` messages. Multiple panes (in this gridwell or other gridwell browser tabs) can attach to the same tile simultaneously; all see the same stream and all can send input. This is a free consequence of CDP being multi-subscriber; no coordination layer.
- Into a **dormant** tile: the pane shows `preview_jpeg` scaled to the pane bounds with no stream. A "wake up" affordance is visible; the user wakes the tile via right-click, which fires `WakeURL`. The Chromium tab spawns at the tile's stored URL and the pane immediately attaches to the new `URLStream`.

**Viewport.** The Chromium tab's viewport tracks the descended pane's pixel dimensions and reflows on pane resize (`Emulation.setDeviceMetricsOverride`). When the tile is live but no pane is descended into it, the viewport retains its last-descended dimensions; for never-descended live tiles a default viewport of **1280×800** is used until first descent.

**The five tile primitives.**

| primitive | RPC | effect |
|---|---|---|
| **create(url)** | `CreateFile` with `mime_type = text/uri-list`, payload = URL | new tile, born live, Chromium tab spawned, streaming begins |
| **wake** | `WakeURL(tile_id)` | dormant → live; Chromium tab spawned at `url_string` |
| **capture** | `CaptureURL(tile_id)` | live → dormant; Chromium tab closed; `preview_jpeg` frozen at the last frame |
| **fork** | `ForkURL(tile_id, dest_path, dest_view_rect)` | duplicates the tile as a frozen sibling at the current `url_string` + `preview_jpeg`; born dormant. The original is unaffected. Works on live or dormant tiles. |
| **delete** | (existing tile-delete path) | remove tile; preview bytes released with the row |

`WakeURL` and `CaptureURL` are the same right-click gesture from the client's point of view; the client picks the RPC based on the tile's current liveness in its local cache.

**Chromium process model.** The server manages **one headless Chromium process per logged-in gridwell user**. Each user's Chromium is launched with `--user-data-dir=<gridwell-data>/chromium/<user_id>/`, giving that user a persistent, isolated profile (cookies, localStorage, IndexedDB, service workers, eventual extensions). This is a multi-user convenience layer, not a security boundary. Processes are lazily spawned on first `WakeURL` / `CreateFile`-for-URL by that user, kept alive while the user has any live URL tile, and torn down when their last live tile is captured or when the user logs out and a configurable grace period elapses.

**Hard boundaries in v1.**

- **Downloads blocked.** Any download triggered by a page is canceled at the CDP `Browser.downloadWillBegin` event. No bytes leave Chromium.
- **File uploads blocked.** `<input type="file">` clicks do nothing usable; file-picker calls are denied.
- **Popups blocked.** `window.open` and `target="_blank"` are suppressed via CDP overrides; nothing is opened, no new tiles are spawned.
- **Permissions auto-denied.** Camera, microphone, geolocation, notifications, clipboard read, MIDI, sensors — all denied via `Browser.setPermission`.
- **JS dialogs auto-dismissed.** `alert` / `confirm` / `prompt` are auto-canceled via `Page.handleJavaScriptDialog`; `beforeunload` is suppressed.
- **Audio muted.** Chromium is launched with `--mute-audio`. Pages play silently; no audio path exists.
- **Page-initiated fullscreen blocked.** Fullscreen API requests are denied.
- **Right-click is gridwell's.** Right-click is the liveness toggle and is never forwarded to the page. Other browser-chrome shortcuts (Ctrl+T, Ctrl+W, Ctrl+L, Ctrl+F, F5, F12) are likewise not passed; they retain their gridwell meanings (if any). Pages cannot use right-click for their own context menus.
- **URL scheme allow-list.** Only `http` and `https` are accepted at `CreateFile` time and on in-page navigation; `file:`, `data:`, `chrome:`, `javascript:`, `about:` and the like are refused.
- **Failed loads** (DNS, cert, 4xx/5xx, infinite redirect, hung renderer) — no special handling. Whatever Chromium paints (an error page, a blank, the last good frame) is what the preview captures. The invariant promises "what was on screen," not "successful content."
- **No URL editing.** `url_string` is not directly editable by the client. It is set by `CreateFile` at creation time and mutated thereafter only by the server's Chromium driver in response to in-page navigations. The "Edit file content" gesture is refused on URL tiles.

**Chromium-absent behavior.** If the server's Chromium binary is missing or fails to launch, URL tiles created previously still render their last `preview_jpeg` in parent grids; descent into them shows the frozen preview with a "Chromium not available — wake disabled" overlay. `CreateFile` for `text/uri-list`, `WakeURL`, and `URLStream` open requests all return an explicit error code the client surfaces in the UI.

### 8.4 Creation Gestures

All creation goes through the **+ palette**. A circular `+` button hovers in a corner of every pane. Clicking it opens a small popover with four tile templates, left to right: well, markdown, URL, upload. The user **drags** a template tile out of the popover onto empty grid space; on release the new tile is placed at the snapped cell (1×1 footprint by default). Template-specific behavior:

- **Well**: commits immediately on drop via `CreateWell`.
- **Markdown**: drop opens a `prompt()` for a title; on confirm, `CreateFile` is fired with `text/markdown` content `"# <title>\n"`.
- **URL**: drop opens a `prompt()` for the URL; on confirm, `CreateFile` is fired with `text/uri-list` and the URL as the payload. The server stores the URL in `url_string`, leaves `blob_id` null, and immediately spawns a Chromium tab for the tile (born live, per §8.3).
- **Upload**: drop triggers the hidden `<input type="file">` picker; on selection the file's bytes are read in the browser and posted as `CreateFile` with a MIME type inferred from the browser-supplied type or the filename extension (limited to the v1 set).

There is no `contextmenu` gesture (the browser's context menu is suppressed on the canvas) and no OS drag-and-drop file upload — all uploads route through the + palette's "upload" template.

### 8.5 Resize

- Right-press on a tile (outside the inner-third center zone), drag, release. The pin is the corner of the original tile diagonally opposite the click quadrant; the cursor (snapped to whole cells) defines the moving corner. New footprint = bounding box of (pin, cursor) with each side ≥ 1. While dragging, the client displays a ghost rectangle that snaps to whole cells. Release commits via `ResizeTile`.
- Refused if the new footprint would overlap any other tile or fall outside the framed view (grids are infinite, so off-edge isn't a concern; overlap and locality are the only checks).
- Resize never scales content. Internal viewport is preserved.

---

## 9. Storage Layer

### 9.1 SQLite Schema

```sql
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;

CREATE TABLE users (
  id               INTEGER PRIMARY KEY,
  username         TEXT NOT NULL UNIQUE,
  password_hash    TEXT NOT NULL,
  primary_group_id INTEGER NOT NULL REFERENCES groups(id),
  root_grid_id     INTEGER NOT NULL REFERENCES grids(id),
  created_at       INTEGER NOT NULL
);

CREATE TABLE groups (
  id   INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE
);

CREATE TABLE user_groups (
  user_id  INTEGER NOT NULL REFERENCES users(id),
  group_id INTEGER NOT NULL REFERENCES groups(id),
  PRIMARY KEY (user_id, group_id)
);

CREATE TABLE grids (
  id         INTEGER PRIMARY KEY,
  object_id  TEXT NOT NULL,
  owner_id   INTEGER NOT NULL REFERENCES users(id),
  group_id   INTEGER NOT NULL REFERENCES groups(id),
  mode       INTEGER NOT NULL,
  refcount   INTEGER NOT NULL DEFAULT 1,   -- number of well-tiles pointing here
  -- saved viewport for fresh descents:
  default_view_cx REAL NOT NULL DEFAULT 0,
  default_view_cy REAL NOT NULL DEFAULT 0,
  default_zoom    REAL NOT NULL DEFAULT 1.0,
  created_at INTEGER NOT NULL
);
CREATE INDEX idx_grids_object_id ON grids(object_id);

CREATE TABLE tiles (
  id            INTEGER PRIMARY KEY,
  object_id     TEXT NOT NULL,
  grid_id       INTEGER NOT NULL REFERENCES grids(id),
  type          TEXT NOT NULL CHECK (type IN ('well','file')),
  x             INTEGER NOT NULL,
  y             INTEGER NOT NULL,
  w             INTEGER NOT NULL DEFAULT 1 CHECK (w > 0),
  h             INTEGER NOT NULL DEFAULT 1 CHECK (h > 0),
  view_x        INTEGER NOT NULL DEFAULT 0,
  view_y        INTEGER NOT NULL DEFAULT 0,
  -- Intrinsic view zoom ratio: window-independent. 0 is the sentinel
  -- "never visited" — the client substitutes a calibrated default for
  -- preview rendering and re-entry.
  view_zoom     REAL NOT NULL DEFAULT 0,

  -- well-only:
  child_grid_id INTEGER REFERENCES grids(id),
  capped        INTEGER NOT NULL DEFAULT 0,

  -- file-only:
  mime_type     TEXT,
  blob_id       INTEGER REFERENCES blobs(id),
  -- URL tiles only (mime_type='text/uri-list'): the current URL, mutated by
  -- the server's Chromium driver as the live tab navigates. Not content-
  -- addressed; not refcounted. NULL for all other file types and for wells.
  url_string    TEXT,
  -- URL tiles only: the latest captured JPEG preview frame. Mutable BLOB,
  -- overwritten in place (does not flow through the content-addressed blobs
  -- table). NULL for all other file types and for wells.
  preview_jpeg  BLOB,

  owner_id      INTEGER NOT NULL REFERENCES users(id),
  group_id      INTEGER NOT NULL REFERENCES groups(id),
  mode          INTEGER NOT NULL,

  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,

  CHECK (
    (type = 'well' AND child_grid_id IS NOT NULL AND mime_type IS NULL AND blob_id IS NULL AND url_string IS NULL AND preview_jpeg IS NULL) OR
    (type = 'file' AND child_grid_id IS NULL AND mime_type IS NOT NULL AND (
      (mime_type = 'text/uri-list' AND blob_id IS NULL AND url_string IS NOT NULL) OR
      (mime_type <> 'text/uri-list' AND blob_id IS NOT NULL AND url_string IS NULL AND preview_jpeg IS NULL)
    ))
  )
);
CREATE INDEX idx_tiles_grid_id   ON tiles(grid_id);
CREATE INDEX idx_tiles_object_id ON tiles(object_id);
CREATE INDEX idx_tiles_child     ON tiles(child_grid_id);

CREATE TABLE blobs (
  id        INTEGER PRIMARY KEY,
  hash      TEXT NOT NULL UNIQUE,    -- sha256 hex
  size      INTEGER NOT NULL,
  mime_type TEXT,                    -- optional hint
  data      BLOB NOT NULL,
  -- Refcount starts at 0; the insert-blob-then-bump pattern in CreateFile
  -- and UpdateFileContent treats the bump as a separate step, which keeps
  -- the find-or-insert branches symmetric.
  refcount  INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE sessions (
  token      TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL REFERENCES users(id),
  expires_at INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX idx_sessions_user_id ON sessions(user_id);
CREATE INDEX idx_sessions_expires ON sessions(expires_at);
```

### 9.2 CoW Fork Procedure

When a write must apply to a tile `N` in grid `G`, and `G.refcount > 1`:

```
BEGIN TRANSACTION

  G' := INSERT INTO grids (object_id, owner, group, mode, refcount=1, defaults...) VALUES (G.object_id, G.owner, G.group, G.mode, 1, ...)

  for each tile M in G:
    INSERT INTO tiles (object_id, grid_id=G', type, x, y, w, h, view_x, view_y, child_grid_id, capped, mime_type, blob_id, owner, group, mode, ...)
      values copied from M.
    if M.type == 'well':
       UPDATE grids SET refcount = refcount + 1 WHERE id = M.child_grid_id  -- new well points at same child grid; bump its refcount

  -- find the well in the editing pane's path that pointed at G; redirect it.
  -- Editing pane's path is provided by the client in the request; the well is the deepest one in the path.
  W := pane.path[-1]    -- the well through which we descended into G
  UPDATE grids SET refcount = refcount - 1 WHERE id = W.child_grid_id
  UPDATE tiles SET child_grid_id = G' WHERE id = W.id
  -- (this UPDATE may itself need to recurse if W's grid is also shared)
  -- Use recursive logic, propagating up the path.

  apply the original write to the corresponding tile in G'.

COMMIT
```

The recursion up the path stops at the first grid in the path that has `refcount == 1` (no clone above this point in the user's tree).

When forking, blob references are not duplicated; `blobs.refcount` is incremented for each new tile row that references a blob.

### 9.3 Reference Counts

- `grids.refcount`: number of well-tiles whose `child_grid_id` equals this grid's id, plus 1 if this grid is a user's `root_grid_id`. The "+1 for root" prevents accidental garbage-collection of root grids that are momentarily unreferenced during ascent-at-root.
- `blobs.refcount`: number of tile rows referencing this blob via `blob_id`.

When refcount drops to 0:

- Grid deletion cascades: remove all tiles in the grid (each tile's child grid or blob has its refcount decremented, possibly recursively deleting them).
- Blob deletion: simply delete the row.

A `FillWell` on an empty well is the only operation that intentionally drops a grid's refcount to 0 (the well is deleted, removing the only reference to its child grid).

### 9.4 Locality Enforcement

Every mutating RPC includes the originating pane's framed rectangle (in the grid's own coordinates) and descent path. The server:

1. Validates the descent path is well-formed (each well in the path actually points to the next grid).
2. Validates that the affected tile's footprint **intersects** the framed rectangle (one cell of overlap is enough). `CapWell` and `RedigWell` apply the stricter rule that the well is **entirely contained** in the rectangle — those operations affect rendering of the whole well, not just a corner, so the user must see all of it.
3. Validates permissions on every relevant grid/tile.
4. Performs the operation, including any CoW forking, in a single transaction.

---

## 10. Client (Go-WASM)

The client renders to a single `<canvas>` and drives all input from there. No DOM beyond `index.html`, the login form, and the OS file `<input>` for upload (which is invisible and triggered by the `+` button's "Upload" option).

### 10.1 Rendering

- Cell size: `64` logical pixels at zoom `1.0`. Configurable constant.
- For each pane: clear, compute visible cell range from `(cx, cy, zoom)`, query a local cache of tiles for that grid, render each visible tile.
- Tile rendering:
  - **Well, open**: a bordered rectangle showing a downsampled rendering of its child grid's framed region.
  - **Well, capped**: distinct visual — covered look, no descent allowed, no preview of contents.
  - **Well, no-read**: padlock tile, distinct from capped.
  - **File, markdown**: thumbnail of the rendered markdown, cropped to `view_y` and the tile's footprint.
  - **File, image**: cropped image at `(view_x, view_y, w*cell, h*cell)`.
  - **File, uri-list (URL tile)**: `preview_jpeg` cropped/scaled to the tile's footprint. A subtle live indicator (small dot or faint pulsing border) distinguishes live tiles from dormant ones (§8.3). Fetched lazily via `GetTilePreview`; invalidated on the `preview_updated` Subscribe event.

### 10.2 Local Cache

The client keeps a per-grid cache of tiles. It populates it via `GetGrid` on first descent, and updates it via `Subscribe` events.

Recursive previews (well showing its child grid) require fetching the child grid as well. The client lazily fetches child grids one level deep when zoom is high enough that the preview would be legible.

### 10.3 Input

Gridwell is **mouse-only by design**. Every gesture has a pointer equivalent; the keyboard is reserved for future text-editing modes (the markdown text-mode `<textarea>` overlay handles its own keys natively).

- **Left mouse button** — content gestures:
  - **Mousedown on empty grid space**: arms a pan. Cursor movement past `dragThreshold` pixels promotes to a pan-drag.
  - **Mousedown on a tile**: arms a tile drag. If the cursor is inside an open well's child preview, the drag source is the child tile under the cursor (the "pull out of well" gesture).
  - **Bare click** (no movement) on a well: descend. On a markdown file: file-descent. On any tile: select. In the outer edge band of a descended/file-focused pane: ascend.
  - **Click on the + button**: open the creation palette popover for that pane.
  - **Click on the file-mode toggle** (lower-right in a file-focused pane): swap between `"text"` and `"rendered"` mode.
- **Right mouse button** — pane-management gestures (§4.1): swap (inner third), split (outer ring near an edge), resize (band along a shared edge), tile cap/redig/fill (right-press on a tile's inner third), tile resize (right-press on a tile outside its center; the diagonally opposite corner is the pin).
- **Mouse wheel**: zooms the pane under the cursor, centered on the cursor. Inside a focused file, the outer ring zooms `FileZoom`; the inner area scrolls (rendered mode) or is handled by the textarea (text mode).
- **Browser context menu** is suppressed on the canvas (`contextmenu` event preventDefault'd).

Modifier-drag for clone is **not yet wired** (see §4.2); when added, the natural binding is `Alt`.

### 10.4 Pane Tree State

The pane tree is **not** persisted across reloads. On boot the client builds a fresh tree with a single pane and recovers the focused pane's state from the URL, which the client maintains via `history.replaceState` (debounced) on every state-mutating gesture.

URL shape (handled by `client/url`):

```
/                                root grid, stored default viewport
/3/4/5                           descended through tiles 3, 4, 5 (well leaf)
/3/4/5?x=12.5&y=-3&z=1.5         grid leaf, viewport center + zoom
/3/4/5/9                         file leaf (rendered mode)
/3/4/5/9?c=24&r=10               file leaf, text mode, cursor at col 24, row 10
```

Path segments are tile row ids in descent order from the user's root grid. The trailing id may resolve to a well-tile or a file-tile; the client resolves which by walking the ids against the cache after decode. Presence of `c`/`r` implies "file is in text mode with the cursor here"; their absence (on a file leaf) means rendered mode. Defaults `x=0`, `y=0`, `z=1` are stripped from the encoded URL to keep it short.

URL-walk robustness: an id that is no longer present in the current grid is silently skipped — the walk stays in the current grid and tries the next id. A capped well or a file mid-path ends the descent at the deepest still-valid prefix. After applying, the client `replaceState`s the cleaned URL so what's in the bar matches what's on screen.

In-memory-only state that survives across descent/ascent but not reload: the per-pane saved-state stack (so within-session ascent restores the exact pre-descent viewport) and `fileLastMode` (so previews remember the user's last-used mode for a file until reload).

The user's root-grid viewport (camera position and zoom) **is** persisted server-side: a debounced `SetGridDefaultView` fires whenever the focused pane is at root and the user pans / zooms. On boot, if the URL has no viewport for root, the client picks up `grids.default_view_cx/cy/default_zoom` and starts the user where they last were.

### 10.5 Login Flow

If no session cookie or session is invalid, the client renders a centered HTML login form (the only HTML UI besides the canvas). On successful `Login`, it tears down the form and starts the canvas client.

---

## 11. Build and Run

```
make build       # builds gridwell binary AND compiles the client to web/gridwell.wasm
make test        # runs all unit tests
make test-cover  # runs all unit tests with coverage
make serve       # builds, then ./gridwell serve --db ./gridwell.db
make clean       # removes the built binary, .wasm, and wasm_exec.js
```

`make build` chains two targets: `bin` (`go build -o ./gridwell ./cmd/gridwell`) and `wasm` (`GOOS=js GOARCH=wasm go build -o web/gridwell.wasm ./client/wasm`). The `wasm` target also copies `wasm_exec.js` from `$(go env GOROOT)/lib/wasm/` (or `misc/wasm/` on older Go versions) into `web/`. Both targets are phony so `go`'s build cache decides what to recompile; this guarantees we never serve a stale artifact.

The `serve` subcommand serves the static directory (default `./web`) at `/`, with SPA fallback (any non-`/rpc/*` path that doesn't match an on-disk file is served as `index.html` so the WASM client owns deep links like `/3/4/5`), and JSON-RPC at `/rpc/...`. Flags: `--db PATH`, `--addr ADDR`, `--static DIR`, `--insecure` (omits the `Secure` flag on the session cookie for local HTTP development).

---

## 12. Testing

**Unit tests are required for absolutely everything that can reasonably be tested.** This is not a polite suggestion — it is a hard requirement of the project. A function that is not tested is treated as a defect. Code review (whether human or AI) must reject changes that add untested logic. The only acceptable exemptions are:

- The WASM `main` entry point (the few lines that wire up `syscall/js` callbacks) — but every function it calls must be tested.
- Generated code (proto stubs, etc.).
- Trivial getters or constructors with no branching.
- Code paths that genuinely cannot be exercised without a running browser, and only after that code has been factored to push as much logic as possible into testable subpackages.

If a piece of logic is hard to test, that is a signal to refactor it — extract the logic from the I/O, inject dependencies, isolate the side-effecting parts behind small interfaces — until it becomes testable. Do not skip the test; fix the design.

Concretely, every package has unit tests with the following minimums:

- `internal/store`: full coverage of CoW fork, refcount maintenance, locality enforcement, permission checks, every mutation type, every refusal path (overlap, capped, empty-only, permission-denied), and the schema's CHECK constraints. Use an in-memory SQLite (`:memory:`) per test. Property-based tests for invariants.
- `internal/auth`: password hashing/verification (including timing-safe comparison), session creation, expiry, and revocation.
- `internal/server`: each RPC method tested with a fake `store` interface — happy path, every error path, every permission case. URL-tile RPCs (`WakeURL`, `CaptureURL`, `ForkURL`, `GetTilePreview`) and the `URLStream` WebSocket tested with a fake Chromium-driver interface that yields scripted frame and navigation events; the real Chromium driver is exercised by a small integration test gated on the Chromium binary being available.
- `client/...` (non-WASM packages): pane tree manipulation (split, close, focus, resize), local cache reconciliation against `Subscribe` events, descent path validation and truncation when tiles are deleted, drag/drop coordinate logic, markdown layout pass.
- `client/wasm`: factor everything possible into subpackages tested under standard `go test`. The `wasm/main.go` entry point should be a thin wiring layer.

Property-based tests (using `gopter` or hand-rolled, or table-driven with randomized inputs) are required for:

- CoW forking preserves observable state from each clone's path.
- The no-overlap invariant after random sequences of create/move/clone/resize.
- Refcount on grids and blobs equals the actual reference count after random sequences of every mutation.
- Pane-tree manipulations preserve the "at least one pane always exists" invariant.

Coverage target: **90%+ line coverage** across all non-generated, non-`main` packages, measured by `go test -cover`. CI fails below this threshold. Coverage is a floor, not a ceiling — the goal is *meaningful* tests, not coverage theater.

Every bug that is found and fixed must come with a regression test that fails before the fix and passes after. No exceptions.

---

## 13. Behaviors Reference

| Operation | Permission required | Refusal conditions |
|---|---|---|
| Read a grid | r on grid | — |
| Descend into well | r on well + r on its child grid | well is capped |
| Create well | w on parent grid | new footprint overlaps existing tile |
| Create file | w on parent grid | overlap; unsupported MIME; oversized |
| Move tile within grid | w on grid | new footprint overlaps |
| Move tile across grids | w on source grid + w on dest grid | overlap on dest |
| Clone tile | w on source tile + w on dest grid | overlap on dest |
| Resize tile | w on tile | new footprint overlaps |
| Set viewport | w on tile | — |
| Cap well | w on well | already capped |
| Redig well | w on well | not capped |
| Fill well | w on parent grid | well is non-empty |
| Edit file content | w on file | type is read-only (image); uri-list URL is not editable via this RPC — it mutates only via in-page navigation in the live Chromium tab |
| Wake URL tile | w on file | Chromium unavailable; tile not uri-list |
| Capture URL tile | w on file | tile not currently live; tile not uri-list |
| Fork URL tile | w on source + w on dest grid | overlap on dest; tile not uri-list |
| Ascend at root | always allowed (creates new root) | — |

In every case, locality of action is also enforced: the affected tile's footprint must lie within the originating pane's framed rectangle.

---

## 14. Out of Scope (v1)

These are designed-around but not built:

- Search.
- Cross-user sharing UI (data model supports it via group permissions).
- Override feedback when concurrent edits collide (current behavior: snap to canonical).
- Group-management CLI commands (`addgroup`, `usermod`, `chmod`, `chown`).
- Tile-pyramid caching for extreme zoom-out (lazy regeneration of previews is enough for v1).
- History navigation across `object_id` lineage.
- Mobile/touch input.
- URL-tile downloads (block in v1 — future: save as a gridwell file tile adjacent to the URL tile).
- URL-tile file uploads (block in v1 — future: bridge a gridwell file picker or the user's local file picker).
- URL-tile audio (mute in v1 — future: opus or WebRTC audio bridged from the headless Chromium to the descending client).
- Per-tile permission grants (camera-allowed tiles, mic-allowed tiles, etc.).
- Extension installation in the per-user Chromium profile.
- Find-in-page, devtools, and a "true reload" RPC distinct from capture-then-wake.
- Configurable User-Agent and per-tile viewport overrides.
- Persisting URL-tile liveness across server restarts (currently all tiles start dormant after a restart).

---

## 15. Definition of Done

- `gridwell init`, `gridwell adduser`, `gridwell serve` all work end-to-end.
- A logged-in user can create wells and files, descend, ascend, cap, redig, fill, move, clone, resize, set viewports, and edit markdown.
- Two browser sessions for the same user see each other's changes via `Subscribe`.
- Permissions model is enforced for all operations (verified by tests; the UI does not yet expose ways to set non-default permissions).
- All unit tests pass.
- `go vet` and `staticcheck` clean.
