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
- **Dependency policy**: Go modules from the standard ecosystem are permitted, but the running system depends only on the Go toolchain at build time and SQLite at runtime — no other external programs, no Chromium, no cgo. The pure-Go SQLite driver is a Go module vendored at build time; it imposes no system dependencies. There is no headless browser in the system; URL rendering relies on the user's browser only.
- **Auth**: cookie-based session. Cookie is `HttpOnly`, `Secure` (in production), `SameSite=Strict`. Session token is a random 32-byte value, stored hex-encoded.
- **Testing**: every package has unit tests. Server logic tested at the service-method layer with an in-memory SQLite. WASM client logic factored so non-DOM code is testable under standard `go test`.

Project layout:

```
/cmd/gridwell/               main.go — subcommand dispatch
/internal/server/            HTTP server, RPC handlers, session middleware
/internal/store/             SQLite schema + queries; CoW logic
/internal/auth/              password hashing (argon2id), session creation
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

Both wells and files have `(view_x, view_y)` describing what region of their interior is currently framed by their footprint.

- For a well, `(view_x, view_y)` is in cells. The well's `w×h` footprint exposes a `w×h` window of the child grid starting at `(view_x, view_y)`.
- For a file, `(view_x, view_y)` is in pixels relative to the file's full rendered content.

**Resize exposes/hides content; it does not scale.** Making a tile larger reveals more of its interior at the same scale; making it smaller hides some.

The internal viewport is persistent state on the tile. Changing it is a write. Snapshots taken on ascent reflect the current viewport.

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
- Each pane has its own state: descent path (list of `(well_row_id)` from root to the currently-viewed grid), viewport center `(cx, cy)` in cells (floating point), and zoom level (floating point, continuous).
- **Keyboard focus** is a single pane. **Mouse focus** is a (possibly different) single pane, determined by hover.
- Splitting a pane clones the current pane's state into both halves. After the split, the panes evolve independently.
- Closing a pane discards its state.
- Pane-tree state is *ephemeral*: it lives only in the client. On reconnect/reopen, the client restores its last-saved pane tree from `localStorage`. Server is not involved in pane state.

### 4.1 Operations on Panes

- `Split horizontal` / `Split vertical`: creates a sibling pane. The new pane starts as a copy of the originating pane (same descent path, same viewport, same zoom).
- `Close pane`: removes the focused pane.
- `Focus pane`: directional movement (e.g., `Ctrl+arrow`).
- `Resize split`: drag the split bar.

### 4.2 Cross-Pane Drag (Teleport)

To move a tile from pane A's grid to pane B's grid:

1. Both panes must be open and pointing at the relevant grids (achieved via split + walk).
2. The user mouse-drags the tile from A. While dragging, a "ghost" follows the cursor across pane boundaries.
3. On drop in pane B at coordinates `(x, y)`, the system attempts the move. If `(x, y, w, h)` doesn't fit, the drop is refused.

Holding a modifier (`Alt` for clone, default for move) selects clone vs. move. Move and clone share the exact same gesture.

There are no bookmarks, persistent portals, or cross-tree navigation. Every navigation has a real cost paid in walking.

---

## 5. Navigation

- **Move = zoom.** No avatar. The viewport is "where you are." asdw or arrow keys pan the keyboard-focused pane. Mouse wheel zooms it. The viewport is `(cx, cy, zoom)` per pane.
- **Continuous zoom.** Any positive zoom value is allowed. Zoom is a viewport property only — it does not change cell size in storage.
- **Locality of action.** A tile may be operated on only if its footprint is currently visible in the focused pane. Definition: any of its cells lies inside the focused pane's framed rectangle. The client enforces this (gray out controls); the server also enforces it via the view position included in each mutating RPC (§6).
- **Descent.** Click on a well or file → that pane's descent path is appended with the well's row id. The pane's viewport resets to the child grid's last-saved viewport (or `(0,0,1.0)` if none).
- **Ascent.** A keyboard shortcut and an on-screen control. Pops the last entry off the descent path. Refused if path is empty (you're at root) — instead, **ascent at root creates a new root**: a new grid is created, a new well in it points to the old root, and the user's `root_grid_id` is updated.
- **Cell size.** Cell size in storage is unitless integer coordinates. The client renders cells at a default of `64` logical pixels at zoom `1.0`. Implementation hint, not spec: feel free to tune.

---

## 6. RPC Service

Wire types are hand-coded in Go in `internal/rpc/types.go` as JSON-over-HTTP messages under `/rpc/<MethodName>`. All requests except `Login` carry the session cookie; the server resolves it to a `user_id` in middleware. All mutating RPCs return the canonical post-write state of the affected grid(s) so the client can reconcile.

Methods (Go method names; HTTP path is `/rpc/<MethodName>`):

  - **Auth**: `Login`, `Logout`, `Whoami`
  - **Read**: `GetGrid` (returns `Grid` + `[]Tile`), `GetBlob`, `GetURLTitle`
  - **Mutations**: `CreateWell`, `CreateFile`, `MoveTile`, `CloneTile`, `ResizeTile`, `SetTileViewport`, `CapWell`, `RedigWell`, `FillWell`, `UpdateFileContent`
  - **Tree-level**: `AscendAtRoot`
  - **Real-time**: `Subscribe` (server-sent events stream)

Every mutating request includes a `view_rect { x, y, w, h }` field describing the framed region of the pane that initiated the mutation. The server rejects the mutation if the affected tile(s) are not entirely within `view_rect`. This enforces locality of action server-side.

### 6.1 Subscribe

The client opens one server-streaming `Subscribe` per descent-path that any of its panes is currently inside. To minimize complexity, a simpler initial implementation: **one subscription per user**, server pushes any change to any grid the user owns. The client filters and applies to whichever panes need updating.

Events:

Event kinds (`rpc.Event.Kind`):

  - `grid_changed` — a grid the user can see was modified
  - `tile_changed` — a tile was upserted (payload: `Tile`)
  - `tile_removed` — a tile was deleted (payload: `grid_id`, `tile_id`)
  - `grid_forked` — a CoW fork happened; clients update `child_grid_id` refs

### 6.2 Concurrency Semantics

All mutations are SQLite transactions. On conflict, the rule is **snap to canonical**: the client applies mutations optimistically, and reconciles when the server's response or `Subscribe` event reports a different state. No override feedback in v1; the affected tile simply moves to its canonical position.

Drag and drop are client-side only until release. The release issues one mutation RPC. The "ghost" the user sees during drag is purely local.

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

### 8.3 `text/uri-list` (read, plus title fetch)

- The blob is one URL (UTF-8). Despite the MIME name, only one URL per file in v1.
- On descent: the client embeds the URL in an `<iframe>` overlay positioned at the pane's bounds. Subject to the target site's `X-Frame-Options` and CSP — pages that forbid framing simply won't render. The iframe is removed on ascent. No server-side proxy.
- Tile preview (always shown in parent grid): the client requests the URL's title from the server via a `GetURLTitle(url)` RPC. The server fetches the URL (`net/http`, with a short timeout and a sensible User-Agent), reads the first ~64KB, and extracts the contents of the first `<title>...</title>` via regex. The result is cached server-side keyed by URL; the client also caches client-side. The tile is rendered client-side via `fillText`, showing the title and the URL beneath. No screenshots, no headless browser.
- If title fetch fails, the tile shows the URL alone.

### 8.4 Creation Gestures

- **New well**: right-click on empty grid space → context menu → "New well." A 1×1 well is created at the clicked cell with a default empty child grid.
- **New file from OS drag-and-drop**: drop a file onto an empty grid region. MIME inferred from extension/content; refused if not in §8 or oversized.
- **New file via + button**: a circular `+` button hovers in the lower-left of every pane. Clicking it opens a small popover with options: "New markdown," "Upload file...". On selection, the next click on empty grid space places it.

### 8.5 Resize

- Drag a tile's edge or corner. While dragging, the client displays a ghost rectangle that snaps to whole cells. Release commits via `ResizeNode`.
- Refused if the new footprint would overlap any other tile or extend off the grid (note: grids are infinite, so off-edge isn't a concern; overlap is the only check).
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

  -- well-only:
  child_grid_id INTEGER REFERENCES grids(id),
  capped        INTEGER NOT NULL DEFAULT 0,

  -- file-only:
  mime_type     TEXT,
  blob_id       INTEGER REFERENCES blobs(id),

  owner_id      INTEGER NOT NULL REFERENCES users(id),
  group_id      INTEGER NOT NULL REFERENCES groups(id),
  mode          INTEGER NOT NULL,

  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,

  CHECK (
    (type = 'well' AND child_grid_id IS NOT NULL AND mime_type IS NULL AND blob_id IS NULL) OR
    (type = 'file' AND child_grid_id IS NULL     AND mime_type IS NOT NULL AND blob_id IS NOT NULL)
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
  refcount  INTEGER NOT NULL DEFAULT 1
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
- `blobs.refcount`: number of tile rows referencing this blob (via `blob_id` or `preview_blob_id`).

When refcount drops to 0:

- Grid deletion cascades: remove all tiles in the grid (each tile's child grid or blob has its refcount decremented, possibly recursively deleting them).
- Blob deletion: simply delete the row.

A `FillWell` on an empty well is the only operation that intentionally drops a grid's refcount to 0 (the well is deleted, removing the only reference to its child grid).

### 9.4 Locality Enforcement

Every mutating RPC includes the originating pane's framed rectangle (in the grid's own coordinates) and descent path. The server:

1. Validates the descent path is well-formed (each well in the path actually points to the next grid).
2. Validates the affected tile's footprint is entirely within the framed rectangle.
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
  - **File, uri-list**: cached URL snapshot, similarly cropped.

### 10.2 Local Cache

The client keeps a per-grid cache of tiles. It populates it via `GetGrid` on first descent, and updates it via `Subscribe` events.

Recursive previews (well showing its child grid) require fetching the child grid as well. The client lazily fetches child grids one level deep when zoom is high enough that the preview would be legible.

### 10.3 Input

- **Mouse**: hover establishes mouse-focus pane. Wheel zooms the mouse-focused pane. Click selects/descends. Right-click context menu. Drag begins on a tile and follows the cursor across panes. Drag on empty space pans.
- **Keyboard**: arrow keys / asdw pan the keyboard-focused pane. `+`/`-` zoom. `Esc` ascends. `Ctrl+arrow` moves keyboard focus between panes. Modifier-drag clones (`Alt`).

### 10.4 Pane Tree State

Persisted to `localStorage` after every change. Restored on load. Format:

```
{
  "tree": <pane-or-split>,
  "focus": <pane-id>
}
where pane = { id, path: [well_row_id...], cx, cy, zoom }
and split = { dir: "h"|"v", ratio: float, a: pane-or-split, b: pane-or-split }
```

If a stored pane's `path` references a row id that no longer exists (was deleted), truncate the path back to the deepest still-valid prefix. If the entire path is invalid, reset to the user's root grid.

### 10.5 Login Flow

If no session cookie or session is invalid, the client renders a centered HTML login form (the only HTML UI besides the canvas). On successful `Login`, it tears down the form and starts the canvas client.

---

## 11. Build and Run

```
make build       # builds gridwell binary; compiles client to web/gridwell.wasm
make test        # runs all unit tests
make serve       # ./gridwell serve --db ./gridwell.db
```

`make build` runs `GOOS=js GOARCH=wasm go build -o web/gridwell.wasm ./client/wasm` and copies `wasm_exec.js` from `$(go env GOROOT)/misc/wasm/` (or `lib/wasm/` on newer Go versions) into `web/`.

The `serve` subcommand serves `web/` as static files at `/` and JSON-RPC at `/rpc/...`.

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
- `internal/server`: each RPC method tested with a fake `store` interface — happy path, every error path, every permission case. `GetURLTitle` tested against an in-process `httptest.Server` returning various HTML payloads (well-formed, malformed, missing title, oversized, slow, redirecting, non-200).
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
| Edit file content | w on file | type is read-only (image, uri-list) |
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
- Multi-URL `text/uri-list` files.
- Mobile/touch input.

---

## 15. Definition of Done

- `gridwell init`, `gridwell adduser`, `gridwell serve` all work end-to-end.
- A logged-in user can create wells and files, descend, ascend, cap, redig, fill, move, clone, resize, set viewports, and edit markdown.
- Two browser sessions for the same user see each other's changes via `Subscribe`.
- Permissions model is enforced for all operations (verified by tests; the UI does not yet expose ways to set non-default permissions).
- All unit tests pass.
- `go vet` and `staticcheck` clean.
