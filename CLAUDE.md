# Gridwell

Gridwell is a single-tenant personal operating environment. Tiles live on a 2D grid; drop one at a coordinate and it stays there. A tile's preview is what you see when you descend into it; ascend after editing and the preview shows what you were just looking at.

This file is the contract. The **Invariants** and **Rules for changes** sections are binding — don't violate them without an explicit conversation with the user.

## The guiding rule: things stay where you put them

**This is the deciding factor.** When a technical decision is unclear, the
option that preserves this principle wins — over performance, over elegance,
over implementation convenience. If a design lets something change that the
user didn't change, the design is wrong. (See *Applying the principle* for how
this is meant to drive concrete decisions, with a worked example.)

Gridwell is a physical space. You rearrange it constantly — it is write-heavy
and mutates freely (drop a tile, pan, capture a page, type) — but **nothing
changes except by your explicit action.** Step out of a room and look back
(ascent): you see it exactly as you left it. Step back in (descent): it is
exactly the same. The round trip is idempotent, and that holds for
*everything* — content, view framing, and layout alike. Reading never mutates.

Four faces of one rule:

1. **Placement is persistent.** A tile at (x, y, w, h) stays until something explicitly moves it. No auto-relayout, no resort.
2. **Identity is persistent and stable.** A tile's row id is a permanent handle: editing a tile never moves it (mutation is in-place; the id never changes), so a reference always returns *that* tile. Copies are made only by the explicit **clone** gesture — see *Identity and copy-on-clone*.
3. **Preview = descent target = ascent return.** A well's stored framing *is* its preview; descending restores it; ascending writes it back. One value, read the same way every time.
4. **Mutation is local and reflected.** Every change goes through the store, which fans an event to every open view.

### Applying the principle (the decision method)

When a design choice is in question, run it through one test: **after this
change, does everything the user didn't touch stay byte-for-byte the same, and
does every reference still resolve to the thing it named?** If not, reject the
option no matter how convenient — then find the option that keeps the principle
and pay its honest cost where the user expects to pay it.

Worked example — *why clone is an eager copy* (decided 2026-06-15). The question
was how a reference (an embedded tile, a saved URL, a bookmark) to a tile should
behave when the structure around it is cloned and then edited.

- A copy-on-write design (share a subtree on clone; fork it on the first edit)
  is cheap to clone, but a fork **re-rows** tiles — the edited instance's id
  moves and a sibling inherits the old id. A stored reference can then silently
  resolve to the *wrong instance's* content. That breaks the principle: the
  thing did not stay where you put it.
- No patch recovers it. Object-ids can't distinguish clones (clones share them);
  capturing the whole id-path only narrows the failure, because a fork reassigns
  a *whole grid's* ids at once — there is no stable per-instance id to capture at
  any level.
- The option that keeps the principle: **never reassign ids.** Editing is
  in-place (the id never moves); copying happens only at the explicit clone
  gesture, as an eager, independent deep copy. A tile's row id is then a
  permanent, durable reference.
- The cost lands where the user asked for it: clone is O(rows) of cheap metadata
  (≈ a few ms for ~1000 tiles; blobs are shared by content address, so no
  content is copied), paid exactly at the "make me a copy" gesture. Edits stay
  O(1) — which is what a write-heavy, continuously-mutating workload needs.

The shape generalizes: prefer **in-place, identity-stable mutation**; make the
expensive, identity-creating operation **explicit and eager** rather than lazy
and silent; and pay costs at the gesture that asked for them.

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

## Identity and copy-on-clone

Nothing the user didn't touch ever re-rows. The consequences:

- **A tile's row id is its durable identity.** Assigned once, never changed; editing a tile rewrites it **in place**. So the row id is the stable handle every reference uses — an embed, a deep-link URL, a bookmark *is* that id, and it always resolves to the same tile. (Why this and not copy-on-write: see *Applying the principle* — a fork reassigns ids and lets a reference silently land on the wrong instance.)
- **`version` (int)** bumps on every **content** mutation: the optimistic-concurrency key (a mutation commits only if its claimed version matches disk) and the spine of edit history.
- **Framing is not a content edit.** Panning / zooming / scrolling (the `view_*` / `text_*` window) re-frames the preview *without* bumping version. It is still a real, persisted, in-place mutation — the framing stays exactly as you left it — it just isn't a *content* change.
- **Clone is an eager copy.** The clone gesture makes a deep, independent copy of the tile (and, for a well, its whole child subtree): new rows for the copy, **blobs shared by content address** (no bytes duplicated). There is no structural sharing of regular grids and no copy-on-write-on-edit — so an edit to one copy can never touch another, and no id is ever reassigned. Clone cost is O(rows) of metadata, paid at the gesture (≈ ms for ~1000 tiles).
- A clone may carry the source's `object_id` as a **provenance marker** (these came from the same origin) — used only by the future horizontal-navigation gesture, never for identity or references. The row id is identity.

The store implements this directly: content edits write in place (`checkPathLeaf`, no fork), the clone gesture deep-copies (`cloneSubtree` / `insertTileCopy`), and delete cascades (`deleteGrid`). Grids aren't refcounted (owned 1:1); only blobs are.

## Preview = descent = ascent

All three read the same stored state, so they agree:
- **Well** — row stores `view_x/view_y/view_zoom` (a rect in child coords): preview frame, descent target, ascent return — one value, three jobs.
- **Text** — row stores the doc-space window (scroll offset + size) and mode (rendered/text). The preview crops the re-rendered doc to that window.
- **URL** — row carries a frozen JPEG + URL string. Descent shows the preview (no network). Refresh floats a native Electron WebContentsView; a capture pump mirrors its live frames into the frozen preview other panes render. Ascent freezes via `SetURLState` (a *versioned*, in-place edit of this tile). The live view eats all mouse input over its rect, so ascent/refresh route through the pane's corner circle, not the content box.
- **File/process-well** — the listing + sticky arrangement live in the disposable cache; descent reconciles them against current host contents (which may have changed underneath). Your arrangement is sticky: a reconcile never repositions or re-rows a tile whose host entity still exists (see *Reconcile: remove on confirmed absence, never on a failed read*). A durable frozen preview, for viewing the archive where the host is absent, is future work.

### Reconcile: remove on confirmed absence, never on a failed read

Source-backed grids (fs / proc) reconcile their tiles against live host state, but they still owe the user *"things stay where you put them"* for as long as the host entity exists. So the reconciler obeys one rule:

**A tile is removed only when a *successful* host enumeration shows its entity is gone. A *failed read* never removes — or re-rows — a tile.**

The hazard it guards against: deleting a tile and re-inserting it next pass is not free — the re-insert lands at an auto-grid cell (placement lost) with a fresh row id (identity lost, so any embed / deep-link to it breaks). A transient or permission read error must therefore preserve the tile untouched, not sweep it.

Concretely:
- **Files** — `fssource.Read` bails (changes nothing) on a directory-read error, and otherwise returns *every* dirent (a broken symlink still appears). A file is swept only when a successful `readdir` omits it.
- **Process children** — `procsource.Children` skips any PID it couldn't read this pass, so absence from its result is *not* proof of death. The reconciler confirms a child is gone with `procsource.Exists` (a `/proc/<pid>` presence check: a clean not-present is the only "gone"; any error means "unknown" → keep) before sweeping its tile.
- **The proc `@info` tile** — the well's own process. `Get` failing is *not* "gone" (it also fails on a transient/permission read); `@info` is swept only when `processGone` confirms the PID is definitively absent, exactly like its children.

The same principle governs the live shell stream: a WebSocket close is treated as "session gone" only on the server's explicit signal (1008), never on an abnormal/transient close (`client/shellconn`).

## The mutation surface — mouse-only, no modifiers

- **Descend** — left-click a well / content tile. **Ascend** — middle-click a pane, or right-click its corner circle (drag out to cancel).
- **Move** — left-drag a tile. **Clone** — right-drag from a tile's center. **Resize** — right-drag a tile's outer ring.
- **Delete** — drag a tile onto a blackhole (left or right drag).
- **Create** — drag from the palette. **Pan** — left-drag empty space. **Zoom / text-scroll** — wheel.
- **Go live / refresh URL/shell** — left-click the descent's corner circle (frozen → goes live). The right button is reserved for the universal pane gestures, so a URL/shell descent has no special center behavior — its center is swap, like every pane.
- **Split / swap / resize / close pane** — right-drag in the pane regions; a new split auto-ascends one level. Left-drag a pane boundary resizes but never closes (live tiles stay put). A live overlay is transparent to the right button (it forwards the gesture to the canvas and parks itself) — the shell does this via its DOM overlay, the native URL view via the Electron bridge.

Every gesture shows a live preview of the action and where it lands before release, plus a neutral cancel zone (Esc is only a fallback). Right-click held stationary reveals what's available — the user should never have to remember a gesture. Text tiles take keyboard input (editing); URL tiles forward keys to the page; the canvas itself reads no modifiers.

## Panes are views, not state

A pane is a window into the canvas. The split tree lives in the browser session and is never persisted — reload discards the layout. The URL captures the focused pane's path + viewport (a link to a *place*), not the split tree.

## Storage: durable archive vs. disposable cache

Two SQLite files on one connection (the cache is `ATTACH`ed, so a single transaction spans both):

- **`gridwell.db` — durable.** Your authored canvas: regular grids, tiles, content blobs, and frozen previews (url/shell JPEGs). Copy it whenever the app is shut down and you have a **complete archive**. Identity + schema version live in the SQLite header — `PRAGMA application_id` ("GWeL") + `user_version` — not in a table.
- **`gridwell-cache.db` — disposable.** Projected host state: `fs`/`proc` source grids, their reconciled tiles, the **sticky arrangement** you give them, and `@info` blobs. Regenerated from the host; safe to delete. Persists across runs but is never part of the archive.

The two share one id space, partitioned at `cacheIDBase` (any id ≥ it lives in cache); `schemaOf(id)` routes every id-keyed query to the right file. A main exit well stores its cache source-grid id in `child_grid_id` — a soft cross-file pointer, **no FK** — and `Open` re-resolves it by identity (`fs_path` / `pid`) so the archive opens on a fresh machine with an empty cache. Cache source grids are shared by identity and **never individually GC'd** (disposable), so they aren't refcounted; only the durable main DB is, which *is* the archive-integrity guarantee.

## Architecture — where things live

- `internal/store/` — SQLite store and all invariants: `schema.go` (the prefix-parameterized DDL for both files), `attach.go` (cache attach, `cacheIDBase` / `schemaOf` id routing, exit-well rebind), `migrations.go` (`application_id` / `user_version` + the additive-migration framework), `cow.go` (the copy-on-clone kernels: `checkPathLeaf`, `cloneSubtree` / `insertTileCopy` deep-copy, recursive `deleteGrid`, `tileRefs`, the `swapTileBlob` blob-swap kernel, blob refcounts), `tiles.go`, `move_clone.go`, `url.go`, `shell.go`, `source_*.go` (file/proc wells), `blobs.go`, `trash.go`.
- `internal/server/` — Connect-RPC handlers, the shell PTY WebSocket (`/rpc/ShellStream`), the `/preview/tile/` image endpoint, SSE events.
- `internal/rpc/` + `api/gridwell/v1/data.proto` — wire types and proto. Regenerate with `buf generate` after editing the proto. `IsWellKind` lives here (shared store+client well-kind predicate).
- `internal/tmux/` — shell sessions.
- `client/wasm/` — the WASM UI, split by concern: `input.go` / `right_button.go` (gesture classification + commit), `gesture_draw.go` (in-flight gesture previews), `render.go` (tile/pane/grid drawing), `glyphs.go` (icon vocabulary), `palette_draw.go` (creation palette), `markdown_render.go` (the markdown *painter* — walks the pure layout's draw ops onto canvas + loads real images), `mutate.go` (tile-RPC dispatch), `url_stream_client.go`, `shell_stream_client.go`.
- `client/` pure packages (host-buildable, table-tested): `pane` (tree + split/resize geometry), `cache`, `clientsync` (RPC conflict-vs-log policy), `dragdrop`, `markdown`, `zoomtrans`, `anim`, `palette`, `panebox`, `preview`, `embed`, `url`/`urlnorm`.

The **markdown engine** (`client/markdown`, pure + unit-tested) is a four-stage pipeline: parse (goldmark, GFM) → lower (AST → a document-model `Node` tree) → layout (`Node` + a `Measure` + a `ClassifyFunc` → positioned `[]DrawOp` + height, all logical coords) → paint (`client/wasm/markdown_render.go` scales the ops and emits canvas calls). Keeping layout pure is what makes wrapping, table column-sizing, list indents, and height math `go test`-able against a deterministic measure. Features: GFM headings/bold/italic/strike/code, code blocks with a small built-in syntax highlighter (`highlight.go` — no chroma, ~0 bundle cost), block/inline quotes, ordered/unordered/nested/task lists, thematic rules, hard breaks, tables, tile-embeds (native canvas previews, hit-tested) and real external images. The layout is memoized in the renderer (`App.mdCache`) keyed by content+width.
- `apps/desktop/` — Electron shell (TypeScript): loads the renderer from the server's own origin, hosts live URL WebContentsViews and the JPEG capture pump.

These are implementation choices — swap them freely if they get in the way: copy-on-clone, SQLite, the content-addressed blob store (sha256, refcounted), tmux, Electron, WebSocket/SSE, `Path` on RPCs, browser + WASM UI, goldmark for markdown parsing. (The *principle* — things stay where you put them — is not an implementation choice; it is the contract.)

## Rules for changes

**Invariants (binding — don't break without a conversation):** things stay where you put them (**the deciding factor** — see *The guiding rule* / *Applying the principle*); seven primitives; color grammar (blue / red / brown / green / purple); preview = descent = ascent; a tile's row id is a permanent handle (nothing the user didn't touch re-rows; copies are made only by the clone gesture, eagerly); mouse-only, no modifiers; every gesture has a preview and a cancel zone; panes are views, not state; the durable/cache split (`gridwell.db` is a complete, copyable archive of authored content; the cache is disposable, regenerable, and never required to open it).

**Per-commit gate:** `make check` (go build, go test, the `GOOS=js` wasm build, and the desktop `tsc` typecheck) must be green on every commit. `make check-electron` (xvfb live-tile harnesses) is for changes touching the URL/shell live path.

**When you touch the store:**
- Content mutations take a `Path` + `Version`: `checkTileVersion`, then `checkPathLeaf` (validates the path and that the tile lives in its leaf grid — no fork, the edit writes the tile in place), then the write + `bumpTileVersion`. Framing mutations write in place too but **don't** bump the version. The blob-swap dance (put → repoint column → inc-new/dec-old) lives only in `swapTileBlob`; reach for it instead of hand-rolling.
- Copying happens only on the clone path: `childGridForClone` deep-copies an interior well's subtree (`cloneSubtree`) and shares host-backed source grids by identity; `insertTileCopy` shares the blob (refcount++). Delete cascades through `decTileRefs` → recursive `deleteGrid`. What a tile owns (an interior-well child grid; its text/preview blob) lives in one place — `tileRefs` — used by clone and delete alike; a new tile kind that holds a grid or blob must be added there, and `property_test.go` must generate it. Grids aren't refcounted (owned 1:1); only blobs are. `verifyRefcounts` (counting `blob_id` + `preview_blob_id`) is the leak net.
- Durable vs. cache: authored content goes in the main DB; projected host state (fs/proc grids + their tiles + arrangement + `@info` blobs) goes in the attached cache. Never write host-projected state to the main DB, and never assume the cache exists — it can be deleted between runs. Every id-keyed statement routes by `schemaOf(id)`; never hardcode a bare `grids` / `tiles` / `blobs` for a row that might live in the cache.
- Host deletes go to the trash (`trash.go`), not `os.Remove`; process deletes `SIGTERM`.
- Source-grid reconcile removes a tile only on *confirmed absence* from a successful host enumeration, never on a failed read (which would re-row + re-place it). Probe presence (`procsource.Exists` / a successful `readdir`), don't infer "gone" from a read error. See *Reconcile: remove on confirmed absence, never on a failed read*.
- Shell sessions are keyed by **tile id**, not `object_id`: a PTY can't be forked, so a cloned shell is a screenshot with no session. The shell WebSocket is same-origin — never reintroduce `InsecureSkipVerify`.

**Testing mode — clean breaks still allowed (versioning machinery now in place).** The canonical machinery exists — `migrations.go` stamps `application_id` + `user_version` into the SQLite header and runs an ordered, additive-only migration list (currently empty) — but we are still in testing mode: the user wipes the DB on schema/label changes, so:
- **No historical migrations yet.** A schema change goes straight into the DDL (`tablesDDL` / `systemDDL`); old DBs are deleted — don't author a migration entry for it.
- **No backward-compat shims** in the server, RPC layer, or wasm client. If a field changes meaning, just change it.
- **No "tiles created before X" code paths.**

Prefer the clean break. This ends when the user declares the format **frozen** (exiting testing mode): from then on every schema change is one additive entry in `migrations` that bumps `schemaVersion` — never renaming, dropping, or repurposing a column — and every wire change a compat shim.

**Non-requirements (don't build these):** multi-user / auth; persisted pane layout; a keyboard shortcut layer over the canvas; background fetches (a URL tile opens a live view only on explicit refresh); undo/redo (history falls out of `version` — future work).

## Future work — horizontal navigation across clones

Under copy-on-clone, a clone is an **independent** tile (its own row id) that
carries the source's `object_id` as a provenance marker. Horizontal navigation
is a gesture to jump between the rows that share an `object_id`; vertical
navigation steps through one tile's version history. The row id stays stable
throughout, so both are pure navigation over existing rows — no identity
juggling required.
