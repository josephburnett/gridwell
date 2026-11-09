# Gridwell

Gridwell is a personal space: tiles on an infinite grid, nested, over the
node's files and federated across machines. Four primitives (text, url,
shell, door) and a closed, stable experience over an open set of content. `ARCHITECTURE.md`
says how it works. Read it first. This file says how to change it.

## The rule: things stay as you left them

Nothing changes except by the user's explicit action. Step out and look back
and it is byte-identical; step back in and it is the same. Reading never
mutates.

When a decision is unclear, this rule wins — over performance, elegance, and
convenience. Ask of every change: is everything the user didn't touch
byte-for-byte the same, and does every reference still resolve to the thing
it named? That is why ids are never reassigned, a copy is deep, framing
never bumps `version`, and there is no move across a connection.

## The promises

The rule above is the first of these. Each is one sentence a user could
check without reading code, and each is held by counts the build runs
(`test/boundary`) rather than by reading for it. A difference the
code makes that no promise or decision here allows is a hole, whatever the
comment beside it says.

- Things stay as you left them.
- A tile behaves the same wherever its content comes from: yours, a
  file's, or a far node's. The only difference is where the bytes come
  from and who owns the address.
- The node learns of a change by being told, never by asking on a clock. A
  clock is for a source that cannot tell, and then it is the source's.
- An open grid shows what its source knows. A change the source has learned
  reaches every open view without a gesture.
- A link is a path. It resolves one hop at a time along one-way connections,
  to any depth, and a break at any hop reads as dead, never as an error.
- What you can see, you can copy. What the holding node can reach, you can
  link. Anything else is refused with the reason, never stored broken.
- Nothing is done for nobody: no capture, fetch or walk runs unless
  something on screen or in the store needs its result.

## The principles

The promises are about behaviour. These are about the code, and each is
held by a mechanism the build runs, or says that it is not yet. A principle
with no mechanism is not held: the ledger (How to work 10) is where it
gets a count, and the assessment's (`.claude/skills/assessment`) only
job is to find the next one (2026-10-21, 2026-10-22).

1. **Things stay as you left them.** Reading never mutates, every user
   write claims what it saw, and a file a released build wrote opens in
   every later build with every user fact intact. Held by the store write
   funnel, the claim refusals and the migration chain with its fixtures.
2. **One fact, one owner.** Every fact is stored or derived once and read
   from there; a copy is a hole. Held by the tables the code
   derives from (`columns.go`, the kind table in `api/rpc/types.go`), the
   boundary tests on the API server, dial and store shapes, and the
   copied-constant and doc-name counts. Not yet held for the client's
   caches.
3. **Every case is a cell.** Behaviour is kinds times operations times
   owners, and every cell is "same" or a decision named here; a decision is
   a row edit, never a sentence. Held by the parity table, at zero
   (`TestParityIsZero`), and by the flag guard on the proto (`rpc.Flags`,
   `TestFlags`): every wire flag is an axis the table carries, or a
   presentation fact with its reason, and the `axes` row counts the
   uncarried ones.
4. **Nothing silent.** No verb an owner leaves unanswered, no empty cell,
   no failure that only logs, no clock asking a source that can tell, no
   work for nobody. Held by the verb count, the timer list, the log-only
   arm list and the trace budget (`trace-budget.spec.ts`, at zero).
5. **Small enough to hold whole, and only shrinking.** Lines, packages,
   comment share and policy-file size are ceilings, so a fix removes as
   much as it adds. Held by the ledger's ceilings.
6. **Decisions in pure code, everything else glue.** A branch the user can
   see lives where a unit test can call it; the shim and Electron main
   gather inputs and perform effects. Held in part by the boundary tests;
   the shim is still checked by reading.

## Vocabulary

One word per thing, and the same word in the UX, the code, the wire and the
docs. A thing with two names is a hole. A word that only restates a
plain phrase goes; use the phrase. A word that leaves goes on
`scripts/retired-words.txt`, and `scripts/check-vocabulary.sh` fails the
build where one comes back outside the paths it allows. Identifiers, wire
names and MCP tool names use the word (`CopyTile`, `Find`, `MoveTile`;
the MCP tools `copy`, `find`, `move`); a renamed wire field keeps its
number. The tile kinds, glyphs and text modes are listed once, in
`api/rpc/types.go`, and `test/boundary` pins it. `docs/concepts.md` says what each concept does; this table says
what each one is called.

**The node and its APIs**

| Word | What it names | What it is not |
|---|---|---|
| node | The one process: one id, one `server.yaml`, one `gridwell.db`. | Its home, which is a namespace; its directory, which is `~/.gridwell`. |
| `~/.gridwell` | The node's directory: `server.yaml`, `gridwell.db`, `cache.db`, `web-password`. | A home. |
| API | One interface the node serves: the web API (the browser's calls), the connection API (the socket a connection dials), the MCP API (agents), the shell API (the WebSocket), the content API (the pages of the node's image, video, audio and PDF files), the trace API. The web, connection and MCP APIs each decode onto the one router. | A door, which is a tile. |
| connection | A config row to a far node: an immutable name, a label, how to dial. | |

**Namespaces**

| Word | What it names | What it is not |
|---|---|---|
| namespace | Home, the files or a connection as the node holds it (`internal/namespace`); the segments of an id before the tile's own, and for the files `<node>/~`. | |
| home | The node's own namespace. A connection lands on the far node's home. | The node's directory. |
| files | The node's second source: the host's directories, shown as grids from the roots `server.yaml`'s `files:` declares (`internal/files`). | Home, which holds the rows the user made. |
| root | One directory `files:` declares, with its label: a swatch in the + menu, where the files start. | Home's root grid, where a client lands. |
| directory | A folder on the host, shown as a grid, over which Gridwell tiles sit as the tiles themselves; its door sits in its parent's grid and wears the outside tint. | A grid of home's. |
| source | Home, the files or a connection, as what a grid's rows come from (`SourceInfo`, `EventSourceHealth`); also the tile a copy was made from. The directory or node a source represents has no word of its own: name the thing. | |
| roster | The handshake's list of a node's sources, one `SourceInfo` row each (`HandshakeResponse.sources`): home, the files, then each connection. | The + menu, which draws its swatches from it. |
| cache | What a connection last answered (`cache.db`, `sourcecache`), served while it does not answer. Disposable, rewarmed by use. | Stale; a file's rows, which are node facts. |

**Tiles**

| Word | What it names | What it is not |
|---|---|---|
| grid | A plane of tiles. | |
| tile | One thing on a grid: a text, a url, a shell, a door or a workspace tile. | |
| door | A tile that leads to a grid, its own or, as a link, another's; descending through it pushes a frame. A drop onto a far node links, never moves. `client/door`. | A content tile; an API. |
| content tile | A text, a url or a shell; a pane descends into one and it goes live. | A door; a workspace tile. |
| link | A tile whose content is a path to another tile, drawn dashed, resolved one hop at a time; broken at any hop it reads dead. Deleting one unlinks. A link to a grid is a door. | A copy. |
| copy | A new tile with the same content, copied to any depth when it is made; of a link, another link to the same target. Right-drag copies. | A link; a move, which never crosses a connection or the disk. |
| reference | A Gridwell URL naming a tile or a grid: its place in the client's address grammar under an origin. A browser opens the place; read with the web password it answers the content. A drop onto a shell pastes one. | A link, which is a tile. |
| text | A tile holding a body; its name is the body's first line. | |
| url | A tile holding an address; it goes live on descent. | |
| shell | A tile showing a terminal; it names the tmux it runs in. | |
| workspace tile | A tile holding a layout: a pane tree, with each frame's framing and scroll. | |
| preview | What a tile looks like from outside. A capture is one; a freeze keeps one. | |
| file information | What the disk says of a file, read when it is opened (`FileInfo`): its inode's facts and its type sniffed from its first bytes; and the view that shows it, the text mode `info`. | A listing, which reads no file. |
| hex view | A file's bytes beneath its file information, per row of 16 the offset, the bytes in hex and their UTF-8 decoding, loaded 64K at a time; the text mode `hex`. | The raw source a text's toggle shows. |
| id | A tile's or grid's own segment, qualified by its namespace's: digits for a home row, or `~` plus a file's absolute path, base64url. A namespace's is seven lowercase base36 characters with a leading letter, or a legacy 32-hex. Never reassigned. `docs/ids.md`. | A file's path, which the id encodes. |

**Places and panes**

| Word | What it names | What it is not |
|---|---|---|
| window | The app window: the pane tree a session shows, and its level. | A text tile's scroll. |
| pane | One panel of the window, on a grid or inside a content tile; one has focus. | |
| place | A pane's stack of frames, as the URL names it: an anchor and a path. | A move; a placement. |
| frame | One step of a place: a grid or a content tile reached through a door, with its framing or scroll. | A draw, which is one paint of the screen. |
| level | The workspace tiles the window is inside, outermost first. | A place, the other axis of where you are. |
| crumb | One per frame, in the bar; a click ascends. | |
| bar | The one bottom bar, riding the focused pane (`wsbar`). | |
| focused tile | The keyboard target a plain click lands on. Session-ephemeral. | The selection. |

**Writes**

| Word | What it names | What it is not |
|---|---|---|
| content | A text body, a url address, a typed name, a workspace tile's layout. A write of it claims and bumps `version`. | |
| version | A count of a tile's content changes; a content write claims it and bumps it. | |
| framing | A grid's center and zoom on the row that owns the door. No bump. | |
| scroll | A text tile's scroll position and mode, where it was left. | The window. |
| placement | Where a tile sits: its grid, x, y, w and h. | A place. |
| move | Writing a tile's placement, claiming the placement it read. `MoveTile`, the MCP tool `move`. | |
| capture | A screenshot of a url or shell tile, kept as its preview. No claim, no bump. | A freeze. |
| freeze | Keeping a captured preview until the reconnect; claims the freeze it saw. | |
| selection | The tiles of a grid the user has in hand, on the grid row, shown the same in every pane on the grid. A select (`SetSelection`) claims nothing. | The focused tile. |
| claim | What a write read: a version, a placement, a freeze. | |
| stale | A claim the row has moved past; refused as a conflict. | A client on another build; a cache. |

**Health**

| Word | What it names | What it is not |
|---|---|---|
| dead | A link whose namespace is not declared, or whose target its source says is gone (`gwerr.DeadLink`). Nothing is said and nothing is asked until the declaration or that source's listing changes. | An error; dark. |
| dark | A source declared but not answering right now: a connection that will not dial, or a root or directory that cannot be read right now, never one the OS refuses this user. Its grids serve what the node holds, a connection's from cache and a directory's from its rows, and the bar's chip shows it. Recovers on its own. | Dead; disabled. |
| disabled | A connection the user switched off from its health notice, until the node restarts. Reads as dark, but nothing tries it again, no notice stands, and a write is refused (`gwerr.SourceDisabled`). The files are never disabled. | Dark. |
| waiting | A connection row minted with no root and no error: asked, not answered yet. The probe's timeout ends the wait. | Broken. |
| broken | A connection's door that will not open, whatever the reason: `Info` failed or the probe timed out (`InfoError` set). One tint; `BrokenReason` carries the detail. | Dark. |
| told | A source whose changes reach the node as they happen (`EventSourceHealth.told`): a connection whose stream is up, or files whose watch is healthy. One that is not told has its grids read on every move, and says nothing. | A failure; dark. |
| gone | A directory not on disk that Gridwell tiles are still over, or over a directory under it: its parent lists it from the rows as a grey door (`Tile.gone`), its grid holding those tiles alone. Reads dead once no tile is left. | Dead, which keeps nothing; dark, a directory that will not read. |
| unknown | Not yet known, neither yes nor no: the callers that return `(value, known)` each pick their own safe default. | |
| notice | A surfaced failure (`client/errsurface`); health notices are shown in a row of them. | A strip. |

**Sessions**

| Word | What it names | What it is not |
|---|---|---|
| session | One client screen, by the id the client minted at boot and sends on `Subscribe` and `SetView`. | A tmux; the browser's cookies and storage, which every url tile shares. |
| view | What a session shows (`SetView`): its panes, their places and focus, the level, a label, the grids on screen. | Framing. |
| subscription | The grids a session shows; the sources watch the union of every open session's. | The event stream, which `Subscribe` opens. |
| away | A session whose last stream closed, kept ten minutes (`subscription.AwayFor`), watching nothing. | |
| tmux | The tmux a shell tile names: by its own id, or for a copy by the id its source named. At most one tile live on it. | A session. |

**The menu and the chrome**

| Word | What it names | What it is not |
|---|---|---|
| + menu | The menu on the + circle: the primitives, with a folded strip of templates and swatches above. `client/menu`. | A palette. |
| template | A tile on home's templates grid, shown in the + menu's strip above the swatches. A click opens an ephemeral copy of it; a drag out moves or copies it as any tile. | A swatch; a clipboard. |
| copy swatch | The dashed grey + first in the strip's templates, no row of its own: released over a tile or a pane, it copies that tile in as a template. | A template; a swatch. |
| swatch | One item of the + menu's strip, per door seen from outside: a home, a far home, a root. | A directory inside a root; a template. |
| strip | The chevron strip of the folded + menu, above the primitives: rows of templates, then rows of swatches; folded or open, the menu's own state. | A notice. |
| palette | The colour set (`theme.Palette`). | The + menu. |
| glyph | A namespace's icon, on its swatch, its ghost and the crumb of the grid it roots. | |

## How to work

1. **One fact, one owner.** Before adding a field, map, or piece of state,
   ask where the fact already lives and read it from there. If two layers
   need it, one derives and the rest read. `ARCHITECTURE.md` lists the
   shapes to copy. A change that leaves the copy-count the same has not
   fixed the class; aim for the design where the bug cannot be written.
   An API's socket and server configuration is one such fact with an
   owner, and no harness rewrites it: `server.WebAPIServer` and
   `server.ConnectionAPIServer` are the two shapes, `dial.ClientConn` is
   the one way to dial a connection API, `test/boundary` pins all three,
   and a harness that builds its own serves or dials an API the node never
   runs.
2. **Root-cause first.** State the mechanism in one sentence before touching
   anything. If you cannot name it, keep digging. A retry, a `setTimeout`, or
   a nil-check that hides the nil is not a fix.
3. **Every bug fix is a test first.** A reproducing test that fails for the
   real reason; a fix for the class, not the instance; a commit message that
   answers "why was this not caught?" and closes the gap. If the code isn't
   testable, make it testable.
4. **Test the seam.** A unit test on each side of a contract does not catch
   a contract mismatch. A seam bug's test crosses the seam.
5. **The shim and the Electron main process are where bugs hide.**
   `client/wasm` has no unit tests and `make check` executes none of it.
   Put decisions in a js-free `client/*` package with tests; the shim is
   glue. Same for `apps/desktop/src/main`: pure modules with unit tests, and
   e2e coverage. `webviews.ts` is tested against a fake view in
   `webviews.test.ts` and against the real one in the capture harness; any
   arm you touch there gets a case in one of them.
6. **Errors surface.** A failure that logs and returns looks to the user
   like "it just disappeared." Route through `client/errsurface` or
   `sendError`. A rejected optimistic mutation reconciles visibly.
7. **No client-only state.** Anything the user can change is a server fact,
   written through the store and reflected by an event. The exceptions are
   decided: the session pane tree, each pane's focused tile
   outside a workspace tile, the outer frames of a pane's place, the workspace level stack,
   the browser's own cookies and storage, and the theme — a client view preference, not a fact
   about your things, so it lives in `localStorage` (2026-09-19). Do not add
   to that list without a decision.
8. **DRY is correctness.** If a fix in one place doesn't fix an identical
   behavior elsewhere, unify them.
9. **Commit each logical change on its own.** Never batch. If an
   instruction seems wrong, say so and propose the alternative — never
   quietly do something else. Fix a stale comment in the same commit that
   touches the file.
10. **Simplification is a ledger.** `test/boundary/ratchet.txt` is the
    ledger: one row per measure, each counted by code, with the
    recorded value, its floor, the owner and the mechanism by which the
    count may fall. `make check` fails on a rise and on an unrecorded fall,
    so the ledger is always exact and its diff is the progress report. A
    unit of simplification work is one row: take it in a worktree, lower
    the count by the mechanism the row names and no other, and record the
    new value in the same commit. A count lowered by another means is
    refused like a rise, and a change that lowers one row by raising
    another is refused by the build. A class row falls by the one
    mechanism it names. A ceiling (`lines`, `packages`) falls by any
    change that keeps every decided behaviour: a fact read from the one
    place that holds it instead of written out again, branches that differ
    only in their data folded into one table, a decision made in several
    places moved to one owner, a special case absorbed by the general one,
    two concepts merged where no decided behaviour tells them apart, and a
    path nothing uses deleted. Removing a behaviour this file decides is a
    new decision, never a simplification (2026-10-26). A decision Joe makes
    may raise a ceiling; the rise is entered in `test/boundary/debt.txt`
    with the decision, counted by the `debt` row, and paying it back, by
    those same ways, is the next row of work (2026-10-22). When what is
    left is behaviour Joe decided and none of it is written twice, Joe sets
    the low-water mark where the code is, and the row clears (2026-10-26).
    A row at its floor is deleted and its count stays as a permanent test. The assessment finds a class
    the ledger does not measure and adds it as a row with a count; a
    finding with no count is a note, not a row. There is nothing more to do
    when the ledger is empty (2026-10-21).

## Comments

A comment says what the code cannot: the decision, the constraint, or the
consequence a reader would not guess. If the code says it, no comment.

- Once, at the owner. Every other site names the owner
  (`// see textedit.SaveQueueKey`) instead of restating it.
- No history. What a field was, what was removed, why a bug was not
  caught: commit messages and `reserved` own those.
- No mechanics. Do not narrate what the next lines do.
- A file or package header is one short paragraph: what it owns.
- An exported name gets one sentence. An unexported name that explains
  itself gets none.
- Past five lines a comment is making an argument. State the conclusion
  and the one fact that forces it.
- A stale comment is worse than none. Leave every comment in a file you
  touch true.

`scripts/comment-share.sh` measures it: comment bytes against code bytes,
per file or for the tree. `test/boundary` holds the tree's ceiling.

## Decisions

These were decided deliberately. Do not reverse one without a new decision.

**Data**

- The node is one process with one directory, `~/.gridwell`: one id, one
  `server.yaml` (`id`, `web`, `federation`, `static`, `shell`,
  `disable_shells`, `connections`, `retired_names`, `files`), one
  `gridwell.db`. `cache.db` is disposable. Serve mints what is absent, and that is the only config write.
- The store mints the node's singleton grids (root, scratch, trash,
  templates) when it opens, never on a read; reading never mutates has no
  exception (2026-09-30). The templates grid is an ordinary home grid,
  `templates_grid_id` beside the others in the `system` table, no gesture
  navigating to it; the handshake names it, qualified
  (`HandshakeResponse.templates_grid_id`), and its tiles are the templates
  (Joe 2026-11-09).
- `gridwell.db` holds node facts only: minted ids, layout, framing, the
  user's bytes, connections, tombstones. What a connection last answered is
  cache. `gridwell size` reports what it holds, read-only and safe while
  serving; `gridwell clean` removes what nothing reaches (a grid no root or
  owning door reaches, the tiles on it or on a gone grid, a blob no kept
  row names, an ephemeral visit no reached workspace tile's layout names,
  the trash's included), never the trash, the templates or a tombstone,
  through the delete's own
  teardown and refused whole if a reached row would change, then VACUUMs,
  and only while the node is stopped (`store.Analysis`, `store.Clean`,
  2026-11-08).
- The storage format is additive by default. Dead storage — no released
  binary reads it for a user-visible meaning, shown by grep — may be
  retired by a rebuild migration that preserves every surviving row. A row
  the new shape cannot hold is converted, never deleted. Never delete a DB
  to absorb a schema change. Retired wire fields are `reserved`. The
  contract is `internal/local/store/CLAUDE.md`.
- `version` means the user's content bytes: a text body, a typed url, a
  typed name, a workspace tile's layout. Captures, framing, placement and the
  freeze cause no bump. A url's address follows where the user went: the
  address a live view settles on after a navigation, an ascent or a freeze
  is written as content and claims a version like a typed one, and landing
  where the row already points writes nothing (2026-09-30).
- Every write of something the user made or placed claims what it was read
  at, one claim per fact, because content, placement and the freeze are
  three facts with one owner each (2026-10-14): content (a text body, a url
  address, a typed name, a workspace tile's layout) claims the version and bumps
  it; a move or resize claims the placement the press saw (grid, x, y, w, h),
  so a move never conflicts with typing nor ages a body; a freeze or
  unfreeze claims the freeze it saw; a delete claims the version and the
  placement, so we know what we are deleting. A stale claim is refused with
  the version conflict and the client reconciles to the row on disk,
  surfaced, never silently: a layout replaces the open tree whole
  (`pane.Levels.Claim`, `nav.GestureReconcileLevel`), a move snaps back and
  re-reads its grid (`App.postPlacement`), a refused delete or freeze
  re-reads it. A claim is about the state, not its history: a tile moved
  away and back is where the writer saw it. A write landing where the row
  already is writes nothing and loses nothing. The APIs refuse a user write
  that names no claim (`rpc.UnclaimedWrite`, which the client checks too), so
  only the node's own writers (a reap, the boot sweep) reach a namespace
  without one; the ephemeral cleanup claims the row as its visit's own
  writes left it (2026-10-14, 2026-10-20). A select is the decided exception: it names the tiles it
  adds and removes and claims nothing (2026-10-17). Only framing (pan and
  zoom, the text scroll, content zoom), captures and a select carry no claim. A workspace tile's layout counted as framing
  until this decision.
- A write to a tile carries one operation: a rename, a content zoom, a
  freeze or a writeback. One carrying more, or none, is refused with the
  reason before anything lands, by every owner alike, home's, the files' or
  a far node's (`rpc.OneOperation`, 2026-10-26).
- The selection is a node fact (2026-10-15): the set of a grid's tiles the
  user has in hand lives on that grid's row (`grids.selection`), home's
  grids and a directory's alike, and a far grid's on the far node. It persists
  across restarts. It names only tiles on its grid, so a tile that leaves
  the grid leaves the set and lands elsewhere unselected
  (`store.leaveSelection`). A select names what it adds and what it removes
  (`SetSelection{grid, add, remove, clear, recurse}`) and carries no claim,
  so a toggle lands any time: two selects of different tiles merge
  whichever lands first, two of the same tile are the user and an agent
  disagreeing and the last one wins, and nothing is lost that a tap cannot
  redo; an added tile not on the grid is ignored. `clear` empties the
  grid's set without naming it, before the adds; with `recurse` it also
  empties every grid under the grid's doors, to any depth, within its
  namespace, in one write, telling each grid whose set changed; `recurse`
  without `clear` is refused. `clear` with `all` and nothing else empties
  every set the node holds, one write per namespace (home, the files),
  and goes on over each live connection to the far node, whose sets are its
  rows; asked over its connection API, a node clears its own and forwards
  nothing (`router.clearAllSelections`). The + circle's right-click offers
  it as Clear selections, above Dump logs, which stays the bottom row
  (2026-10-17). It bumps no version, the grid's
  or any tile's, so a select never refuses a text save. A tap adds or
  removes one tile, a sweep adds the band, Esc removes the set the pane
  shows (`gesture.Toggle`, `Sweep`, `Clear`). `SelectionChanged` carries the whole set, so no view re-reads the
  grid to learn it. Inside a workspace tile the selection is the grid row's too,
  as everywhere (2026-10-18): a fact about a grid comes from the grid, two
  panes on one grid show one set, a select in either writes the row, and an
  agent reads and sets what the user sees. A layout holds no set; one a blob
  stored before then carries (`sel`) is read past and not written again,
  since a set is redone with a tap. It is not a pane's focused tile, which a plain
  click moves. Shift + left-click toggles a tile; shift + left-drag on the
  background sweeps a cell-snapped band and adds the tiles wholly inside it,
  never one it only touches; Esc with no drag in flight clears the focused
  grid's set; on touch a long-press lifted in place toggles. Shift is the
  only select key, since ctrl and cmd are the split's
  (`gesture.ClassifySelect`). Shift + left-click on a crumb in the bar
  toggles the tile the crumb stands for, a door, a content tile or a
  workspace tile, on the grid it sits on, through the same toggle, and
  does not ascend; a crumb that stands for a grid toggles nothing; on
  touch a long-press lifted on the crumb is the same toggle
  (`wsbar.RouteClick`, `pane.NavCrumb.SelectTarget`, 2026-11-06). A
  selected tile's crumb wears the border, so the view lists the grids the
  bar's crumbs sit on (`pane.Showing`). A select is drawn on the press and reconciles
  on the verdict like a move; every selected tile wears the yellow border
  (`theme.Palette.Selection`) in every open view (2026-10-15).
- A tile row keeps three times, one owner each, in unix seconds on home's
  rows and a file's alike, none on the wire (2026-10-17): `created_at`,
  when the row was made; `updated_at`, when anything last wrote it (a move,
  framing, a freeze, content); `content_at`, when its content last changed,
  written only by the version bump (`bumpTileVersion`) and read as
  `created_at` until then. Find filters on them, on a grid (`grid_id`,
  `recurse` through owned doors, never a link), on the selection, and on
  the kind (text, url, shell, door or workspace, 2026-10-18; an unknown one is
  refused); the filters AND, an empty query with one lists what passes, newest first, and
  they are the node's, applied to the rows it holds (`store.tileFilter`).
  A text query answers empty on the files. A file row's times are the node's
  (minted, touched), not the file's.
- Framing is a float center plus a pane-size-independent zoom on the row
  that owns the door; a root keeps the same shape on its grid row. One
  wire verb (`SetFraming`), one store writer, one client function. Inside a
  workspace tile a grid's view is per pane, not the row's: each frame of each
  pane's stack keeps its center and zoom in the layout blob, a pan there
  writes the blob on the layout's settle and never the door row, and the
  one-active-surface rule is the home view's only, since no row is shared
  (`pane.FramingOwner.Layout`, 2026-10-14).
- A text tile's scroll is where its doc is scrolled and its mode, never the
  box it is shown in: the pane's size is the viewer's geometry, so a look
  from a pane of another size, on another device, writes nothing (`textedit.Scroll`,
  2026-10-16). `text_w`/`text_h` are dead storage. The row's scroll is
  where the tile was left: the grid preview crops there and a descent opens
  there. Inside a workspace tile the live scroll is the layout's: a scroll there
  writes the blob on the layout's settle, a settle or a focus takes only the
  mode to the row, and an ascent, which is leaving, writes the row's whole
  scroll as it does anywhere; a leaf the blob has no scroll for starts at
  the row's (`pane.FramingOwner.Layout`, `textedit.RowScroll`, 2026-10-16).
- Ids are 7-char lowercase base36 with a leading letter. Both shapes (short
  and legacy 32-hex) are valid forever. The leading letter is how a URL
  tells a namespace segment from a tile id.
- Navigation asks for a grid or a row only when the cache does not hold it
  current: held, from a source that is answering and whose changes are told
  (`cache.Current`, from `EventSourceHealth.told`; a source whose changes are
  not told is read on every move). The
  event stream keeps those true, so coming back to one reads nothing; a stream
  gap still re-reads every held grid. The planner decides
  (`nav` `planner.fetchGrid`), never the executor (2026-10-14).
- A copy is eager and deep. COW was tried and removed. A copy keeps its
  name and whether it was typed, across any namespace boundary as within
  home: a url follows its page title until a rename latches the name, and a
  copy of an unrenamed one still follows it. The row's `alt_user` is the one
  owner, `Tile.named` carries it, only a rename sets it, and a create
  honours it (`store.createTile`, 2026-10-20).
- A copy of a link is another link to the same target, never a copy of the
  target: the deep copy is of content, and a link's content is its path
  (2026-10-06).
- A copy whose source is dark lands as a link to it, never a refused or
  empty copy: the copy reads the source before it creates anything, and a
  source that does not answer gets the link a ctrl + right-drag would make
  (`server.deepCopyTile`, 2026-10-06). What a copy of each kind becomes,
  across sources or within home, is `rpc.CopyOf`'s, which the router's walk
  and the store's copy both read.

**Node**

- Inside the node a namespace is a Go value (`internal/namespace`): home
  (`internal/local`), the files (`internal/files`) and each connection.
  gRPC is used at one hop only, the connection API. Both node-side APIs
  decode onto the one router.
- The node runs no code but its own (2026-11-07, reversing the third-party
  API of 2026-08-15). Besides home it reads two kinds of source, its files
  and its connections, both Go values behind `namespace.Namespace`. Nothing
  is spawned, and nothing under `~/.gridwell/plugins` is created, read or
  deleted. A behaviour the files have is the node's code, not a declaration
  on a wire.
- The repo is one Go module (Joe, 2026-11-09, reversing the separate api
  module): one `go.mod`, one `go.sum`, no `go.work`, import paths
  unchanged. Nothing outside the repo builds against `api`, so its v0.x
  tags stay in git history and none is pushed again. The arrows are package
  arrows and `api` imports only the wire and the standard library, both held
  by `test/boundary` (ARCHITECTURE.md, Package boundaries).
- Under the node an id has two shapes and no third (2026-11-07):
  `<node>/<digits>` is a home row, `<node>/~<base64url of an absolute path>`
  is a file or a directory (`rpc.KeyTileID`, `rpc.TileKey`), and
  `<node>/<connection>/...` is a connection's. The files' owner id is
  `<node>/~`, the key form of the empty path: `rpc.OwnerNamespaceOf` answers
  it for every `<node>/~<path>` id, and it is the one owner rule for
  routing, a shared owner, a dead link, the disable verdict and the health
  key. A directory's grid and the
  door that shows it in its parent both name the directory's path; `GetGrid`
  and `GetTile` are separate verbs, and where one id must be classified the
  disk says whether the path is a directory. A path is the thing's address:
  a renamed or moved file retires its old id and a link to it reads dead,
  since a link means the thing at that address (2026-10-22).
- Roots are config (2026-11-07): `files:` in `server.yaml` is a list of
  `{root: <absolute dir>, label: <text>}`. The roster has one files row
  (`<node>/~`, labelled files, the folder glyph) carrying one grid per root,
  between home and the connections, and each root is one swatch in the +
  menu with its label, in config order. A root is a starting point, not a
  boundary (2026-11-08): every absolute path on the host is addressable
  as `<node>/~<base64url of the path>`, and one that exists is live, the
  node's own directory included, because a user who can get a shell on
  the machine owns it. A `server.yaml` that still has `plugins:` or `routes:`
  is refused at parse with a message naming `files:`.
- The files are one package, `internal/files`, reading disk and the
  store's file rows directly (2026-11-07). A directory's grid takes every
  Gridwell tile, home's rows placed over it, and makes a file or a directory
  on disk only through the file and directory primitives (the place
  decision below, 2026-11-08). A directory is a door; an image,
  video, audio or PDF file, a page file, is a url tile whose page the node
  serves, since none of them runs a script; every other file, HTML, CSS,
  JavaScript, JSON and SVG (which can carry a script) included, is a text
  tile, plain or both by its extension, or info: a file whose name renders
  as nothing, by the one table of what renders as text, is a text tile with
  no body that shows its file information (2026-11-08). A name that is no regular file,
  directory or symlink, a FIFO, a socket or a device, is a text tile with
  no body, read-only because it is not a regular file, and nothing opens it
  (2026-11-08). A file
  never placed sits at the placement its place in the listing derives. A
  write to a file claims its content stamp, `<mtime_ns>-<size>`, and a
  stale stamp is refused as a version conflict; a file the node cannot
  write is read-only with the reason. An image file's preview carries a
  stamp, so a changed file redraws; an image whose header declares more
  than 16 million pixels gets no preview, and its tile shows its name. Delete moves the file
  to the OS trash, by a rename: a file on another filesystem than the home
  trash goes to that filesystem's own, `.Trash-<uid>` at its top
  (`.Trashes/<uid>` on macOS), made if absent, and a delete with no trash
  to rename into is refused with the reason; nothing is copied (2026-11-08).
  Move renames within the files, and the file's row (placement,
  name, framing) follows it under its new path's id while the old id
  retires; a move to a far node is refused, and a Gridwell tile moves
  between home and a directory as between two home grids. Find on the files
  locates by `id:` and applies the node's row filters, and a text query
  answers empty: no body is read for it.
- A file's facts the user made are rows, minted on touch (2026-11-07):
  placement, framing, the typed name, the frozen preview and the content
  stamp. A listing is overlaid onto the rows; every listing of a directory
  is authoritative, so a row whose path it no longer names is swept and
  tombstoned. An id is never reassigned: a path listed again is live again
  under the same id.
- A file's name is its file name until a rename, which the node keeps on
  its row as home keeps one (`alt_text`, `alt_user` latched), claiming and
  bumping the version, and the file on disk is not renamed by it. A text
  tile's name stays the file's, since its presentation reads it
  (2026-10-20). A page the node serves has no address of its own, so a
  navigation in it writes nothing (2026-10-21), and a capture of it keeps
  the screenshot alone, in place of an image's own picture: its title is
  the file's name and it has no history (2026-11-08).
- A directory's door never copies (2026-10-20): copied onto a Gridwell
  grid, home's or a far node's, it lands as the link to its grid a
  left-drag makes, and the ghost shows the link before the drop. Every file
  copies onto a Gridwell grid as a link too (2026-11-08): a page file, whose
  page is the node's, has no address to copy (2026-10-21), and a text
  file's bytes stay on disk, markdown included, since nothing is converted
  between the disk and Gridwell (the place decision below). Within the
  files a right-drag copies the file on disk. A door copies as a link when
  the grid it opens takes no new tiles (`rpc.CopyOf`, read by the node and
  the drop verdict alike), so a disabled connection's door links too, as a
  dark one does.
- A directory is a place for Gridwell tiles (2026-11-08): the filesystem
  is the physical space, and Gridwell's tiles sit over it. Any Gridwell
  tile, a text, url, shell, door or workspace tile, is placed on a
  directory's grid as the tile itself: a home row (`<node>/<digits>`) whose
  grid is the directory's, the `grids` row keyed by its path, its bytes in
  `gridwell.db` and nothing on disk. The Gridwell layer is one space, home,
  every directory and every Gridwell grid under one: a left-drag moves
  within it, so a text tile moved onto a directory moves and does not cross
  into the disk; a right-drag copies within it; ctrl + right-drag links.
  Nothing is ever converted between the disk and Gridwell: a file never
  becomes a tile and a tile never becomes a file, which drops the markdown
  copy of the same morning both ways. A file or a directory dragged onto
  any Gridwell grid, home's or a Gridwell door's inside a directory, is a
  link, because disk things never live on a Gridwell grid; within the files
  a left-drag renames and a right-drag copies the file. The ghost shows
  each verdict before the drop, and `rpc.CopyOf` owns them, read by the
  node and the drop verdict alike. A directory's grid answers
  `accepts_tiles` true, so the + menu offers every primitive there, and two
  more: file and directory, each asking for a name before it is made, in
  the url visit's modal (`openEntryModal`, `menu.DiskCreate`; an empty name
  makes nothing), an
  empty file by O_EXCL or a directory by mkdir, a name already there
  refused with the reason, and offered only while the directory is
  writable, the grid's second accepts fact, the disk side's. A shell
  created on a directory's grid starts its tmux in that directory, read
  from the grid its row sits on when the tmux starts; a home grid's starts
  where the node does. `write_text` on a directory's grid makes a Gridwell
  text there, never a file; the MCP has no verb that writes the disk. Tint
  is the only tell: a file wears the outside colour, a Gridwell tile its
  kind's, in a directory as in home.
- A directory moved within Gridwell carries its overlay (2026-11-08):
  every Gridwell row under it, subdirectories included, follows to the new
  path, since their grids are keyed by the path that moved. A directory
  that goes away underneath Gridwell, deleted, renamed or moved outside it,
  stays in its parent as a gone door (`Tile.gone`), grey, the muted family
  (`pane.TileFamily`), holding only its Gridwell tiles, its grid still a
  directory's and tinted so: the one door a listing does not supply, made from the
  rows, up to the root, so no Gridwell data is lost to a change of the
  disk; its tiles are moved out by hand. Deleting a directory within
  Gridwell sends the directory to the OS trash and every Gridwell tile
  under it, recursively, to the Gridwell trash grid, where every deleted
  tile is found again; restoring one does not restore the other. Deleting a
  Gridwell tile on a directory's grid alone is the Gridwell trash, as in
  home; deleting a file is the OS trash.
- A symlink is a link (2026-11-07), followed wherever it goes
  (2026-11-08). One to a directory is a door whose
  grid is the target's, drawn as a link because the grid is not its own;
  one to a file is a link tile whose target is the target's path id,
  `~<base64url of its absolute path>`, and a path through a symlinked
  directory resolves. Either is dashed, opens the target, and is dead
  only when the target is gone.
- A directory wears its own tint, the outside colour, a dark red in both
  themes of `theme.Palette`, a hue no kind uses, because a directory is
  outside the system (2026-11-07). A door whose grid is a directory wears
  it wherever it sits, as does every file in a directory, page files
  included. The swatch, the ghost, the door, the crumb, the bar band and
  the pane border all read one classifier (`pane.TileFamily`,
  `pane.FamilyOf`) and one table (`theme.Palette.Family`), so no two of
  them can disagree (2026-11-08). The client reads that a grid is a
  directory from its id's shape (`rpc.IsFilesID`), never from a wire flag.
- The files are dark or told, never disabled (2026-11-07). The node
  watches only the directories some open session shows, with a 200 ms
  debounce and a re-walk on overflow, and tells each change as
  `GridChanged` or `TileChanged`; told is whether that watch is healthy. A
  root or directory that cannot be read right now is dark: its grid serves
  what its rows hold and says so through the health event, as a dark
  connection's serves its cache. One the OS refuses this user (`EACCES`,
  `EPERM`) is refused with the reason on its grid, and on its door once a
  read of it was refused, since a listing opens no subdirectory; it is not
  dark, and the files stay told (2026-11-08, Joe's Mac trace, the Trash on
  macOS). Nothing watches a directory nobody shows, and nothing
  reads one unasked: a recursive Find is the user asking, so it reads the
  directories under the grid it names, never through a link, and a read for
  a Find says nothing of the files' health (2026-11-08).
- The node's rows of every namespace but home's are dropped (2026-11-07):
  one rebuild migration deletes every tiles and grids row of a namespace
  other than home's and retires the `ns` column. This is the one decided
  exception to converting a row the new shape cannot hold; the files start
  fresh.
- The handshake's roster carries one row per source, home, the files and
  each connection, and the health event one per source, the files' keyed
  `<node>/~`; their messages are
  named for sources and keep their field numbers (2026-11-07). The
  repository says plugin only where `scripts/retired-words.txt` allows it:
  a `reserved` comment, a migration, a stored spelling (the `plugin_uuid`
  system key, the `plugins:` key the config refuses) and a historical
  ledger.
- A connection is config: an immutable name, a label, how to dial.
  Retiring a name is forever, and explicit: `retired_names:` is the one
  owner, and the stored `deleted` flag is only that list mirrored. Boot
  never retires a name because the config stopped declaring it — the row
  and its landing stay, its links go dead by the roster, and the stanza
  brings them back. Secrets stay host-local file paths.
- A trace dump carries the node's `server.yaml` (2026-11-07): the bytes on
  disk at dump time, comments and order kept, appended after the ring's
  records in the one `.jsonl`, so a reader of a dump sees the node's files
  and connections without asking. The yaml holds paths to secrets,
  never a secret, and the web password is a file of its own that no dump
  reads.
- A source that answers is not dark; dark comes only from a read the
  source did not answer, and a read the caller abandoned says nothing about
  it (`gwerr.IsAbandoned`) (2026-10-02). Whether a source's changes are told
  right now is one fact, `EventSourceHealth.told`, and it is the one fact the
  client's cache reads (`cache.Current`). A declaration is never a notice;
  only a failure is (2026-10-25).
- The connection cache walks a far node's home on every subscribe and
  again when the connection comes back, so a far home reads while its
  connection is down. It is the decided exception to "nothing is done for
  nobody": the store needs the result, because offline readability means
  all of the far home, not only what was visited
  (`sourcecache.kickPrefetch`, 2026-10-06). The walk is home's only, never
  the far node's files (2026-11-08): it stops at a door onto a far
  directory and at any link, because a files root can be a whole home
  directory and a link can lead anywhere, even back. A far directory reads
  live when opened, and while dark serves what the cache last answered for
  it, if it was opened while connected.
- A stream is established by its header, never by time: a source is back
  when it says it is listening, so a change made before then is not missed.
  Both ends of a connection run a build that sends it; a far node that sends
  none reads as dark until it is upgraded (`namespace.Follow`, 2026-10-14).
- A client tells the node what it shows as one view per session
  (`SetView`): its panes, each pane's place and whether it has focus, the
  window's workspace level, a label ("desktop", "browser"), and the grids on
  screen, which the client derives because the layout rects are its own. The
  view is the subscription's one owner: the union the sources watch derives
  from it, and the node holds it in memory per session while a stream
  carrying that session is open, never stored, so the pane tree stays
  session-ephemeral (`subscription.Book`, `Server.Sessions`, 2026-10-15). The
  node keeps an away session's view for `subscription.AwayFor` after its last
  stream closes, listed as away and watching nothing, so an agent sees a
  phone that went to the background, marked away in `sessions` (2026-10-18).
- A node has no grid of its own. A connection lands on the far node's home.
  The + menu's top row is one swatch per declared door: a node's home and
  a connection's far home are places and get a row, and each root is one
  (`client/door.PlacesOf` enumerates, `client/menu.Doors` composes). A
  root carries its framing, so it reopens where it was left. That row is an open set, so it is
  folded: a menu opens on the primitives with a chevron strip for the section
  above it, and every opening starts folded — the fold is the menu's live
  state and dies with it (`client/menu` decides what a state shows and owns
  the flag).
- The + menu's strip holds templates above the swatches (Joe 2026-11-09):
  rows of at most five, as the primitives are, the templates' rows first,
  the copy swatch then the templates newest first, then the swatches' rows,
  the two never sharing a row; three rows show and the rest scroll in the
  strip. A template is a tile and is drawn as it is on a grid. Left-drag,
  right-drag and ctrl + right-drag of one move, copy and link it as of any
  tile, a drop on a shell pastes its reference and the trashcan deletes it,
  so the strip behaves as a copy buffer. A click opens an ephemeral copy:
  the copy is made on the scratch grid first, deep as every copy is (a
  shell's naming the template's tmux), and the visit descends into it, for
  every kind, so the template's row, preview included, is never touched.
  The copy swatch released over a tile, a content pane, a pane inside a
  workspace tile, an ephemeral visit's pane or a shell copies that row onto
  the templates grid by `rpc.CopyOf`'s rules, the ghost showing the verdict;
  released anywhere else it does nothing. Dragging a tile into the strip is
  not a gesture. A view whose strip is open lists the templates grid, so
  the node watches it only then.
  The templates are this node's on every menu, a far node's grid's included,
  from the handshake; newest first is by row id, highest first, since home
  mints ids in creation order and no time is on the wire; a new template
  lands at x 0, its own height above the topmost (`menu.TemplateCell`), so
  the templates grid opened as a place reads newest first as the strip does;
  a copy onto the scratch grid shares a cell, as a visit made there does,
  so two clicks on one template both land. A click on the copy swatch does
  nothing, the menu stays open after a drop on it, and since it is always
  in the strip the fold toggle shows whenever the primitives do.
- Agents reach the node through the MCP API (2026-10-15): `/mcp` on the
  web API, Streamable HTTP, the web password as a bearer beside the cookie,
  and no build gate, since no page is behind it. It decodes onto the one
  router (`internal/mcpapi`): every write reads first and claims what it
  read, every refusal is a tool error with the router's reason, and a tile it
  creates or copies with no cell named lands at the first free cell
  (`store.FreeSlot`). It
  exposes no destroy from the trash, no shells and no config;
  `docs/mcp.md` is the contract; `read_grid` with the id `templates` reads
  home's templates grid (2026-11-09). It has ten tools (2026-10-18): `sessions`,
  `read_grid`, `read_tiles`, `find`, `write_text`, `rename`, `move`, `copy`,
  `delete` and `select`, and every write takes a list and answers a verdict
  per item in order, each item its own write with its own claim, a refused
  one stopping none after it. A session answers only where the user is
  (2026-10-18): `sessions` lists every screen showing the node, newest
  change first, each with its workspace tiles and its panes' places, grids and
  focus, and marks yours the one holding the agent's shell live; it chooses
  none, and no screen is an empty list. What a grid holds and has selected
  is the grid's, from `read_grid`, so "here" is a focused pane's grid or
  workspace tile and "these" is that grid's selection; `read_tiles` on a workspace
  tile answers the panes its layout holds, decoded by the client's own
  decoder (`pane.DecodeLayout`). A new tmux carries
  its key in `GRIDWELL_SHELL`, Claude Code sends it as `X-Gridwell-Shell`,
  and the shell API, the one place a socket and its session are both in
  hand, joins the two (`Server.ShellHolder`).
- A connection the user disables, from the Disable button on its health
  notice, stays disabled until the node restarts. It is held in the node's
  memory only, never in `server.yaml` or
  `gridwell.db`, nothing tries it meanwhile, and it reads as dark, with no
  notice standing (2026-10-09). Disabled is a verdict, not an outage: the
  router refuses a write to it with `gwerr.SourceDisabled`, which the client
  drops without parking or retrying (an optimistic one silently), and every
  grid it serves is stamped `writable=false`, `accepts_tiles=false`, each
  row `read_only` with the reason, so an edit is refused before it is typed;
  a merely-down source keeps parking writes in the outbox (2026-10-09).
- The web API always has a password (the minted 0600 `web-password` file;
  delete it to rotate). The connection API is a 0600 unix socket, never
  TCP. Its `server.yaml` key stays `federation:` and its file stays
  `federation.sock`: an existing `~/.gridwell` already has them written down.
- The node serves a page file's page through its content API
  (2026-11-07), and every other file's bytes as
  `application/octet-stream`, which no browser runs (2026-11-08): a GET or HEAD of `/content/<token>/<qualified tile id>`
  streams the file in chunks as it is read, with its media type, `nosniff`,
  the sandbox CSP and its `Content-Length` when its size is known, and
  honours one byte range (`Range`: 206 with `Content-Range`, 416 past the
  end, a list of ranges answered whole), so a video plays and seeks as it
  arrives and no page is held whole (2026-11-08); nothing else: no subpath,
  no POST, no other request header carried. A permission the disk refuses
  answers 403.
  The token in the path stays, because the desktop's url views run in their
  own partition and cannot present the web cookie, and it is never
  interchangeable with the cookie. ServeContent is the bytes, or one range
  of them, the size and media type of one tile's page and crosses a
  connection, so a far node's image opens
  here. `serves_page` is the one flag that says a url tile's page is the
  content API's, and only a url tile carries it.
- A place URL is a reference (Joe, 2026-11-08): a GET of one whose
  `Accept` does not lead with `text/html`, carrying the web password as a
  bearer or the cookie, answers what the place names: a text's body
  (`text/markdown`, a file that is not markdown `text/plain`), a url's
  address (`text/uri-list`), a page's bytes through the content API, a
  door or a grid as `read_grid` answers it and any other tile as
  `read_tiles` does (`mcpapi.Reader`); a browser's navigation still gets
  the client. The path is read by `pane.DecodeURL`, the client's own
  grammar, never a second parser. Like the content API it is outside the
  build gate: a tool's read names no build. The MCP instructions say how
  to read one, and docs/mcp.md says the same words.
- A drop onto a shell is one verb, `PasteShell{tile_id, tile_ids,
  origin}` (Joe, 2026-11-08), which the web and connection APIs decode
  onto the router and the MCP API does not expose. The node owning the
  shell's tmux answers it: its router refuses a row of another node
  (`shellpaste.Refusal`), reads the rows and spells the text
  (`shellpaste.Text`): a file or a directory its absolute path, any other
  tile its reference under the client's origin or, with none, as a far
  node is sent none, under its own web address (`shellpaste.BindOrigin`),
  which a shell on its machine reaches; each word single-quoted where a
  shell would split it, space-separated, no newline. Home types it with
  `tmux send-keys -l`, attached or not, so a frozen shell takes a drop; a
  tmux that is not running is refused and none is started, since a paste
  is not an open. The files refuse it and a connection forwards it.
- A url tile opens its own address, and a link followed inside a live page
  navigates that page (2026-11-07, reversing the site pages and address
  rules of 2026-10-25). A link that opens a window opens its address in a
  pane below.

**Experience**

- A pane's place is one stack of frames. Descending through any door
  pushes; ascending pops. The URL and the layout blob encode the stack; the
  crumbs project it. The workspace level is the one second axis.
- Every stacked level stays alive. Descending into a workspace tile parks the
  outer level; resources close only when the pane closes. One live surface
  per content tile — opening it elsewhere takes over.
- Descent goes live. The frozen preview is what a tile looks like from
  outside; entering a url reopens it, entering a shell reconnects. One
  owner decides (`shellconn.DecideAutoLive`). The freeze gesture is the
  standing exception, for url and shell alike: it is a screenshot, so the
  tile keeps that preview, the address and the tmux keep existing, a
  copy taken while frozen keeps the copy, and only the reconnect clears it
  (`shellconn.DecideGoLive`).
- A file whose name renders as nothing shows its file information
  (Joe, 2026-11-08). Its preview in a directory is its size, as `ls -h`
  spells it, off the stamp, and the type its extension says
  (`rpc.ExtensionType`), so a listing reads no file. A descent asks
  `ReadContent{info}` and shows the inode's facts and the type sniffed
  from its first 512 bytes; see more opens the hex view beneath, which
  asks the content API for 64K at a time by range as it is scrolled.
  Read-only: nothing edits the bytes. Any other file reaches its file
  information by the i button beside the rendered/raw toggle. The info and
  hex modes are the row's text mode, so a file reopens as it was left
  (`textedit.DecideDescent`, `client/fileinfo`). A file's information is
  read when it is opened and never cached for a dark source.
- An ephemeral visit is a tile with no home grid and no other difference
  (2026-10-20): a visit writes its address, title, freeze, name and content
  zoom wherever its row is, and offers the freeze gesture and the rename
  (`urlview.WriteAddress`, `urlview.Writeback`, `contentzoom.Decide`,
  `barslot.Decide`, `bartitle.Decide`, none of which decides a write from
  whether a row is ephemeral). Ephemeral means retirement only, which the grey border and
  the title's "— ephemeral" say. The row is deleted when the visit ends and
  nothing holds it, no other pane and no workspace tile's layout; the
  delete rides the row's save chain behind every write on the row and
  claims the row as they left it (`outbox.SaveQueue.EnqueueAfterHolds`).
- Shells ride the web API: a WebSocket at `/shell`, same cookie, same
  origin. Every host with the web client has shells — a phone reaches them
  through the browser, like everything else. The only thing that turns
  shells off is the node (`disable_shells`).
- A shell tile names a tmux: its own id, or for a copy the tmux its
  source named (`rpc.ShellTmux`). Any number of tiles may name one
  tmux; at most one is live, and going live on another takes over
  (close and reopen; within one tile the terminal moves). A tmux no tile
  has a preview of was never started, and whichever tile opens it first starts
  it; a started one that is gone is never recreated behind a preview. The
  tmux dies when the last row naming it is destroyed. A shell copied
  across nodes lands as a link (2026-10-01).
- Session-ephemeral: the pane tree, each pane's focused tile (the keyboard
  target a plain click lands on, `pane.SessionState.Selected`; not the
  selection, which is a node fact), the level stack, and, outside a workspace
  tile, the outer frames' viewports. The durable home for a
  layout is the workspace tile, and inside one every frame's viewport is part of
  it (2026-10-14).
  Inside a workspace tile the focused pane and the zoomed pane are part of the
  layout the tile keeps, written with it on every change, so a workspace
  reopens on the pane it was left on; the focused tile is
  session-ephemeral only outside a workspace tile (2026-10-14,
  `pane.LayoutFingerprint`).
- Left-drag moves, right-drag copies, ctrl + right-drag links — the
  modifier flips the right button from copy to link, in the namespace the
  drop lands in or across one. Across a connection a left-drag links too,
  because there is no move across one; between home and a directory a
  Gridwell tile moves, since the Gridwell layer is one space (2026-11-08).
  A right-drag of a directory's door links, because a directory never
  copies (2026-10-20), and so does any file's onto a Gridwell grid, because
  nothing is converted between the disk and Gridwell (2026-11-08). Dashed means link; deleting a link
  unlinks. Ctrl or cmd + left-click descends in a new pane split below —
  the same split a link out of a live tile opens — in any pane; macOS
  reports ctrl + left as a right press with ctrl held, so a ctrl or cmd
  right-click on a tile that never becomes a drag is the same gesture
  (2026-10-09, `gesture.SplitNav`). A press that
  crosses the drag threshold is a drag, even if it drops on its own cell:
  it snaps back and is not a click
  (2026-09-30, `dragdrop.DropRejected`). Esc cancels any drag in flight:
  everything returns to where it was at the press, and the release is a
  no-op (2026-10-01, `gesture.Escape`). A drag or a trashcan drop on a
  selected tile acts on its grid's whole selection, and on an unselected
  tile on that tile alone, leaving the selection be: the set keeps its
  layout, one offset for all, and is refused whole when any tile would land
  on a cell the set does not vacate; each tile is its own write with its
  own claim, so a stale one is refused and snaps back alone
  (2026-10-15, `dragdrop.CarryOf`).
- A drop onto a shell pastes (Joe, 2026-11-08): a release over a shell,
  its tile on a grid or its live pane, whatever the button, types into its
  tmux what the carry names and moves nothing; only a menu template, a
  bare click and the trashcan stand above it (`dragdrop.DropPaste`). It
  lands when every carried row is of the shell's node, and is refused
  otherwise with "<name> is not on the shell's node", which the ghost
  reads from the ids alone and writes under its no-entry badge; a paste
  that lands outlines the shell.
- A finger does not move or delete a file (2026-11-08): on a touch press,
  a file or a directory dropped on another directory's grid, a rename
  across directories, or on the trashcan is refused with the reason ("a
  finger does not move or delete a file; use a mouse"), the ghost wearing
  the no-entry badge and snapping back, nothing asked of the node, because
  a stray finger should not reshape the disk. A finger still places and
  resizes a file within its grid, copies or links it, creates from the +
  menu, and does anything to a Gridwell tile wherever it sits; a mouse does
  all of it (`dragdrop.FingerNoFile`, the press's `DropInput.Touch`).
- A link is a path, and a link broken at any hop — a namespace this node
  does not declare, one a node further along the chain does not declare, or
  a target its namespace says is gone — is dead: greyed, inert, still
  labelled, still deletable. It is a state, not an error — nothing is asked
  for it and nothing is said about it (`gwerr.DeadLink` is the answer,
  `client/deadlink` the verdict). A gone key listed again is live again under
  the same id. Dead is not dark: a root that
  cannot be read right now and a declared connection that will not answer are
  health, and they come back. Nor is dead always forever: a retired
  connection name never returns, but a namespace merely undeclared is live
  again the moment it is declared again. `client/deadlink`.
- One bar, at the bottom of the window, always visible, riding the focused
  pane: it spans that pane and slides under it as focus moves, inside a
  full-width row reserved once, so no pane ever resizes. Panes end at that
  row's top edge; a left click on the background beside the bar focuses
  the most recently focused pane of the column above it, that recency being
  part of the session's pane focus (2026-10-09, `wsbar.BandFocus`). A crumb
  click ascends; middle-click is the in-pane shortcut.
- A click lands on what it hits, in any pane, and pane focus follows in
  the same press (2026-10-02, reversing 'moves focus, nothing else'). No
  press is focus-only. Keyboard focus is set once, by the surface that
  took the press, and nothing deferred may move it before the next press
  (`gesture.Land`, `takeKeyboard`). The right button acts where it was
  pressed, and a right press over a live url view moves pane focus as any
  press does (`EV.rightPress`). A right press over a live page is the
  site's unless it becomes a drag (2026-11-06): a right-drag that starts
  anywhere over the view is the pane gesture it would be on the canvas, on
  every platform, and a release without a drag hands the page its own
  `contextmenu`, held from the press where the platform fires it there
  (`urlview-preload.ts`). Gridwell pops no menu of its own over a page; the
  circle's right-click is the pane's menu.
- A left-drag on a pane boundary resizes it. A press grabs at most one
  divider per axis, so at the corner of three panes it grabs both and the
  one drag moves both: two ordinary resizes sharing one gesture, rather than
  a gesture of its own. `pane.GrabDividers`.
- A right-drag from a pane's edge splits it. Inside a workspace tile the outline
  at the window edge is part of the pane it borders, so an edge is as easy
  a target as an interior divider (2026-10-09, `pane.GutterPane`).
- A grid's framing is decided when the user frames it (2026-10-20). The
  stored zoom is the live zoom over the pane's overtake
  (`zoomtrans.Writeback`), and no size change (a window resize, a divider
  drag, a split) rescales a pane's live view, so a pane keeps showing the
  pixels it showed. A pane whose live view has not moved since it last
  settled writes no framing, whatever its size now: a split writes nothing
  from either pane, and the next pan or zoom in a pane writes the ratio at
  that pane's size. A grid's first visit writes nothing either, so the row
  keeps no framing until the user frames it. The settled view is the
  frame's: a navigation lands it, a framing write takes it, and a split's
  copy carries it (`pane.Frame.FramingWrite`).
- Pane closing is progressive crush: drag-through with red warning.
- The rendered view is a sanitized HTML overlay. Task-list checkboxes are
  the one interactive control; everything else is read-only.
- A rendered pane's picture holds no form control: a task checkbox is drawn
  by the one stylesheet, as the overlay's input is, because iOS WebKit
  paints an SVG image holding a control as nothing while it loads
  (`markdown.PictureXHTML`, 2026-10-16).
- A pane shows one document whether or not it has focus. The focused pane
  shows the cached bytes; everything else drawn from them is a picture of them (a wrap, a
  raster) keyed by `cache.BodyGen`, which every change to the bytes moves,
  an unsaved edit included, so no picture outlives the bytes it was made
  of. A slot's last picture stands in only while its successor rasterizes
  (`rasterprev.Cache`). A pane's rendered overlay lays the document out at
  its raster's width, scrollbar gutter kept, so it wraps the same lines
  focused or not (`markdown.RenderedInset`, 2026-10-09).
- The browser's cookies and storage are host-local: one set for every live
  url tile. Nothing about them or networks crosses the wire.
- Modals center on the active pane.
- A client the node served runs the node's build: the web API refuses any call
  from another build, and the client shows one persistent notice with a
  Reload button and reloads only when the user asks (2026-10-07).
- An ascent keeps the door in view (Joe, 2026-11-09): "when I ascend
  from a grid ... I would like to automatically zoom to a center point
  such that my previous location is within view, at least 1/3 of the way
  into the screen." The grid's own view is landed, moved by the smallest
  pan that shows the door or content tile the pane came out of wholly,
  its center in the middle third of each axis, and zoomed out to fit it
  only when it cannot fit at that zoom (`zoomtrans.InView`). Every ascent
  takes it, the crumb, the middle click, out of a content tile and inside
  a workspace tile, and a row the landing grid does not hold, an
  ephemeral visit's, moves nothing. The landing is settled, so it writes
  no framing until the user frames the grid; inside a workspace tile the
  layout the ascent writes carries it. A resize moves nothing.

## Gates

`make check` must be green on every commit. It cannot see the native layer,
and that is where the worst bugs live.

| Gate | Sees | Run it when |
|---|---|---|
| `make check` | Go + TS logic; compiles wasm, executes none of it | every commit |
| `make check-electron` | the `WebContentsView` bridge under xvfb | the live url path, `webviews.ts`, the preload |
| `make check-e2e` | the full app as a black box | any `apps/desktop` change, anything under `client/wasm` or `web/`, the native layer, cross-seam behavior |
| `make check-web` | the browser-mode client: caps, touch, shells | `client/caps`, `client/touchgest`, `client/wasm/touch.go`, the browser-serving path and its shell API |
| `make check-connections` | the real binaries through a real ssh tunnel | the files and the dialer, the export, id routing; a dependency or toolchain bump — grpc, x/net, or the go line is every seam at once, so run it locally before pushing, plus the native gates for a desktop (npm) dependency |

A feature is not done when its gates are green. It is done when a trace of
ordinary use (the Dump logs row) has been read and every refused call,
repeated ask and odd id in it is either fixed or a decision written here.

If a change touches the native layer or the shim, `make check` passing says
nothing about it. Run the electron or e2e gate and add a spec. The gates rebuild at
start, and `check-e2e` and `check-web` then test the artifacts they copied at
that moment (`apps/desktop/e2e/runtree.ts`), so a rebuild beside a running one
cannot change what it tests; the other gates have no such pin, so still do
not edit sources while one runs. A flake rerun only counts against a freshly
built tree. A spec on `docs/flake-ledger.md` still gets a fresh look before
you blame a change.

A dependency or toolchain bump commits under the `deps:` prefix. A `deps:`
bump or a shim change names the gates it ran in its message: the bump touches
every seam at once, and nothing in `make check` executes the shim, so in both
cases `make check` alone proves little.

## Before you commit

- [ ] I named the mechanism (fix) or the one owner of any new fact
      (feature) in one sentence.
- [ ] I added no copy of an existing fact.
- [ ] A test fails before and passes after, across the seam.
- [ ] For a bug fix, the commit message says why it was not caught.
- [ ] `make check` is green; the native gates too if I touched that layer.
- [ ] For a `deps:` bump or a change under the shim, the commit message
      names the gates it ran.
- [ ] No error is swallowed. Unacknowledged writes park in `client/outbox`
      and nothing user-made is dropped without a server verdict.
- [ ] Nothing the user can change lives only on the client.
- [ ] This is one logical change, and the comments in the files I touched
      are true and meet the comment standard.
- [ ] Things stay as the user left them.
