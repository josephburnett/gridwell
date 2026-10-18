# Gridwell

Gridwell is a personal space: tiles on an infinite grid, nested, federated
across plugins and machines. Four primitives (text, url, shell, door) and a
closed, stable experience over an open set of content. `ARCHITECTURE.md`
says how it works. Read it first. This file says how to change it.

## The rule: things stay as you left them

Nothing changes except by the user's explicit action. Step out and look back
and it is byte-identical; step back in and it is the same. Reading never
mutates.

When a decision is unclear, this rule wins — over performance, elegance, and
convenience. Ask of every change: is everything the user didn't touch
byte-for-byte the same, and does every reference still resolve to the thing
it named? That is why ids are never reassigned, a copy is deep, framing
never bumps `version`, and there is no cross-plugin move.

## The promises

The rule above is the first of these. Each is one sentence a user could
check without reading code, and each has a lens in the holistic assessment
that enumerates the cases rather than reading for them. A difference the
code makes that no promise or decision here allows is a hole, whatever the
comment beside it says.

- Things stay as you left them.
- A tile behaves the same wherever its content comes from: yours, a
  plugin's, or a far node's. The only difference is where the bytes come
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

## Vocabulary

One word per thing, and the same word in the UX, the code, the wire and the
docs. A thing with two spellings is a hole. A word that only restates a
plain phrase goes; use the phrase. A word that leaves goes on
`scripts/retired-words.txt`, and `scripts/check-vocabulary.sh` fails the
build where one comes back outside the paths it allows. Identifiers, wire
names and MCP tool names spell the word (`CopyTile`, `Find`, `MoveTile`;
the MCP tools `copy`, `find`, `move`); a renamed wire field keeps its
number. The tile kinds, glyphs and text modes are spelled once, in
`api/rpc/types.go`, and `test/boundary` pins it. `docs/concepts.md` says what each concept does; this table says
what each one is called.

**The node and its APIs**

| Word | What it names | What it is not |
|---|---|---|
| node | The one process: one id, one `server.yaml`, one `gridwell.db`. | Its home, which is a namespace; its directory, which is `~/.gridwell`. |
| `~/.gridwell` | The node's directory: `server.yaml`, `gridwell.db`, `cache.db`, `web-password`, `plugins/<id>`. | A home. |
| API | One interface the node serves, or in the plugin's case dials: the web API (the browser's calls), the connection API (the socket a connection dials), the plugin API (what a plugin serves the node), the MCP API (agents), the shell API (the WebSocket), the content API (a plugin's pages), the trace API. The web, connection and MCP APIs each decode onto the one router. | A door, which is a tile. |
| connection | A config row to a far node: an immutable name, a label, how to dial. | |

**Namespaces**

| Word | What it names | What it is not |
|---|---|---|
| namespace | A home, a plugin or a connection as the node holds it (`internal/namespace`); the segments of an id before the tile's own. | |
| home | The node's own namespace. A connection lands on the far node's home. | The node's directory. |
| plugin | A subprocess that serves keys and content and declares its grids; the node owns the ids and the layout, and mints a plugin's grid when it is touched. | A home. |
| entry | One thing a plugin lists: its key and its content. | A plugin's grid. |
| key | A plugin's own name for one thing it serves. | An id, which the node mints. |
| source | A plugin or a connection, as what a grid's rows come from (`source_label`, `DisableSource`); also the tile a copy was made from. The directory, service or node a plugin or connection represents has no word of its own: name the thing. | |
| cache | What a source last answered (`cache.db`, `sourcecache`, a plugin's `state_dir`), served while it does not answer. Disposable, rewarmed by use. | Stale. |

**Tiles**

| Word | What it names | What it is not |
|---|---|---|
| grid | A plane of tiles. | |
| tile | One thing on a grid: a text, a url, a shell, a door or a workspace tile. | |
| door | A tile that leads to a grid, its own or, as a link, another's; descending through it pushes a frame. A drop into another namespace links, never moves. `client/door`. | A content tile; an API. |
| content tile | A text, a url or a shell; a pane descends into one and it goes live. | A door; a workspace tile. |
| link | A tile whose content is a path to another tile, drawn dashed, resolved one hop at a time; broken at any hop it reads dead. Deleting one unlinks. A link to a grid is a door. | A copy. |
| copy | A new tile with the same content, copied to any depth when it is made; of a link, another link to the same target. Right-drag copies. | A link. |
| text | A tile holding a body; its name is the body's first line. | |
| url | A tile holding an address; it goes live on descent. | |
| shell | A tile showing a terminal; it names the tmux it runs in. | |
| workspace tile | A tile holding a layout: a pane tree, with each frame's framing and scroll. | |
| preview | What a tile looks like from outside. A capture is one; a freeze keeps one. | |
| id | Seven lowercase base36 characters with a leading letter, or a legacy 32-hex, qualified by its namespace. Never reassigned. | A key. |

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
| dead | A link whose namespace is not declared, or whose key its namespace says is gone (`gwerr.DeadRef`). Nothing is said and nothing is asked until the declaration or that namespace's listing changes. | An error; dark. |
| dark | A source declared but not answering right now: a connection that will not dial, or a plugin whose directory or service stopped answering while its process still does. Its grids serve from cache, and the bar's chip shows it. Recovers on its own. | Dead; disabled. |
| disabled | A plugin or connection the user switched off from its health notice, until the node restarts. Reads as dark, but nothing tries it again, no notice stands, and a write is refused (`gwerr.SourceDisabled`). | Dark. |
| waiting | A connection row minted with no root and no error: asked, not answered yet. The probe's timeout ends the wait. | Broken. |
| broken | A door that will not open, whatever the reason: `Info` failed or the probe timed out (`InfoError` set). One tint; `BrokenReason` carries the detail. | Dark. |
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
| + menu | The menu on the + circle: the primitives, with a folded strip of swatches above. `client/menu`. | A palette. |
| swatch | One item of the + menu's strip, per door seen from outside: a home, a far home, a plugin's grid. | An entry. |
| strip | The chevron strip of the folded + menu, above the primitives; folded or open, the menu's own state. | A notice. |
| palette | The colour set (`theme.Palette`). | The + menu. |
| glyph | A namespace's icon, on its swatch, its ghost and the crumb of the grid it roots. | |

## How to work

1. **One fact, one owner.** Before adding a field, map, or piece of state,
   ask where the fact already lives and read it from there. If two layers
   need it, one derives and the rest read. `ARCHITECTURE.md` lists the
   shapes to copy. A change that leaves the copy-count the same has not
   fixed the class; aim for the design where the bug cannot be written.
   An API's socket and server configuration is one such fact with an
   owner, not glue each harness rewrites: `server.WebAPIServer` and
   `server.ConnectionAPIServer` are the two shapes, `dial.ClientConn` is
   the one way to dial a connection API, `test/boundary` pins all three,
   and a harness that builds its own serves or dials an API the node never
   runs.
2. **Root-cause first.** State the mechanism in one sentence before touching
   anything. If you cannot name it, keep digging. A retry, a `setTimeout`, or
   a nil-check that hides the nil is a smell, not a fix.
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
   decided, not accidental: the session pane tree, each pane's focused tile
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

## Comments

A comment says what the code cannot: the decision, the constraint, or the
consequence a reader would not guess. If the code says it, no comment.

- Once, at the owner. Every other site names the owner
  (`// see textedit.SaveQueueKey`) instead of restating it.
- No history. What a field was, what was torn out, why a bug was not
  caught: commit messages and `reserved` own those.
- No mechanics. Do not narrate what the next lines do.
- A file or package header is one short paragraph: what it owns.
- An exported name gets one sentence. An unexported name that explains
  itself gets none.
- Past five lines a comment is making an argument. State the conclusion
  and the one fact that forces it.
- Stale is worse than absent. Leave every comment in a file you touch
  true.

`scripts/comment-share.sh` measures it: comment bytes against code bytes,
per file or for the tree. `test/boundary` holds the tree's ceiling.

## Decisions

These were decided deliberately. Do not reverse one without a new decision.

**Data**

- The node is one process with one directory, `~/.gridwell`: one id, one
  `server.yaml` (`id`, `web`, `federation`, `connections`, `plugins`), one
  `gridwell.db`. `cache.db` is disposable. Serve mints what is absent, and that is the only config write.
- The store mints the node's singleton grids (root, scratch, trash) when it
  opens, never on a read; reading never mutates has no exception
  (2026-09-30).
- `gridwell.db` holds node facts only: minted ids, layout, framing, the
  user's bytes, connections, tombstones. What a connection last answered is
  cache.
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
  without one; the ephemeral cleanup claims the row its visit held
  (2026-10-14). A select is the decided exception: it names the tiles it
  adds and removes and claims nothing (2026-10-17). Only framing (pan and
  zoom, the text scroll, content zoom), captures and a select carry no claim. A workspace tile's layout counted as framing
  until this decision.
- The selection is a node fact (2026-10-15): the set of a grid's tiles the
  user has in hand lives on that grid's row (`grids.selection`), home's
  grids and a plugin's alike, and a far grid's on the far node. It persists
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
  every set the node holds, one write per namespace (home, each plugin),
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
  (`gesture.ClassifySelect`). A select is drawn on the press and reconciles
  on the verdict like a move; every selected tile wears the yellow border
  (`theme.Palette.Selection`) in every open view (2026-10-15).
- A tile row keeps three times, one owner each, in unix seconds on home's
  rows and a plugin's alike, none on the wire (2026-10-17): `created_at`,
  when the row was made; `updated_at`, when anything last wrote it (a move,
  framing, a freeze, content); `content_at`, when its content last changed,
  written only by the version bump (`bumpTileVersion`) and read as
  `created_at` until then. Find filters on them, on a grid (`grid_id`,
  `recurse` through owned doors, never a link), on the selection, and on
  the kind (text, url, shell, door or workspace, 2026-10-18; an unknown one is
  refused); the filters AND, an empty query with one lists what passes, newest first, and
  they are the node's, applied to the rows it holds (`store.tileFilter`),
  never a plugin's: the plugin answers the text, and the node keeps the hits
  whose rows pass (`pluginhost` `filterHits`). An entry with no row has no
  time and is in no selection but has its kind, and a plugin row's times are the node's
  (minted, touched), not the source's.
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
  (`cache.Current`; a source with live updates off is read on every move). The
  event stream keeps those true, so coming back to one reads nothing; a stream
  gap still re-reads every held grid. The planner decides
  (`nav` `planner.fetchGrid`), never the executor (2026-10-14).
- A copy is eager and deep. COW was tried and torn out.
- A copy of a link is another link to the same target, never a copy of the
  target: the deep copy is of content, and a link's content is its path
  (2026-10-06).
- A copy whose source is dark lands as a link to it, never a refused or
  empty copy: the copy reads the source before it creates anything, and a
  source that does not answer gets the link a ctrl + right-drag would make
  (`server.deepCopyTile`, 2026-10-06).

**Node**

- Inside the node a namespace is a Go value (`internal/namespace`). gRPC
  survives at two hops only: the plugin subprocess and the connection
  API. Both node-side APIs decode onto the one router.
- The plugin API is the third-party way in, and plugins live in their own
  repository, `github.com/josephburnett/gridwell-plugins` — the shipped fs,
  proc, pages, gitlab, gmail and hey on the same footing as anyone else's.
  This repo owns the plugin API: the proto, `api/gen/plugin/v1`, and the
  go-plugin handshake (`api/compose`).
  No gridwell package, TEST FILES INCLUDED, imports a plugin implementation
  or names that repository in a go.mod, and nothing switches on a plugin
  kind; every plugin behavior rides a wire declaration. `test/boundary`
  enforces it. A test that needs a real plugin spawns the shipped binary
  (`internal/plugintest`). `make build` compiles those binaries out of
  `$(PLUGINS_DIR)` (default `../gridwell-plugins`) into the repo root, so
  check out the plugins repo beside this one.
- A plugin serves keys and content; the node owns ids and layout. A plugin
  holds no node fact, and keeps its own cache of its source in the private
  directory the node hands it at spawn (`~/.gridwell/plugins/<id>`, as
  `state_dir`), under `cache.db`'s contract: disposable, safe to delete,
  rewarmed by use. Nothing deletes one automatically. A connection is
  config: an immutable name, a label, how to dial. Retiring a name is
  forever, and EXPLICIT: `retired_names:` is the one owner, and the stored
  `deleted` flag is only that list mirrored. Boot never retires a name
  because the config stopped declaring it — the row and its landing stay,
  its links go dead by the roster, and the stanza brings them back.
  Secrets stay host-local file paths.
- A plugin is not a place (2026-09-06, reversing the `root_grid`
  decision). Nodes have homes; a plugin contributes doors. Its grids are
  + menu rows, one per grid it declares (`InfoResponse.grids`, each naming
  the grid's key), and it never wraps them in a synthetic root grid of
  doors. `root_grid` is RETIRED: the field number is kept forever and the
  node reads it in one place only — a plugin that answers a root_grid and NO
  grids gets one derived grid onto it, so an old binary keeps presenting.
  Declare both and grids wins; there is no privileged grid. A plugin that
  declares none
  contributes nothing and is healthy, not broken: `pluginhealth` gives its row
  no status at all. A plugin that cannot serve the source its config declares
  refuses `Info` with the reason, and is broken with that sentence and no
  grids until the fix lands, which needs no restart (2026-09-30).
- A plugin entry may be a link to another of its entries (2026-10-02):
  `Entry.link_target` names the target by grid and key, and the node
  draws the entry as a link to the target's key-form address, reads through
  it, and mints nothing for the target until it is touched. A listing is
  authoritative only when it enumerates the grid definitively (a whole
  read, or a whole box walk while the live feed is connected), and `Probe`
  answers for the grid it names, so leaving one grid is not being
  gone. One thing listed in many grids is
  one tile (hey: a thread in everything, its boxes holding links).
- A source that lists but cannot watch is not dark: its listings stay live
  and the refusal is one notice (`EventPluginHealth.live_updates_off`); dark
  comes only from a listing the source did not answer, and a read the caller
  abandoned says nothing about it (`gwerr.IsAbandoned`) (2026-10-02).
- A plugin's cache answers when a refresh fails: the read succeeds from
  what it caches and says so in `ListResponse.unreachable` (the reason,
  empty when live), which the node reports as the source's health while it
  keeps serving the rows, never a failed read the cache could answer. It is the node's own rule — a cached grid
  serves first, and that it is cached is the source's health — one hop
  further (2026-10-02, `docs/plugin-standard.md` rule 7).
- A plugin does work only to answer a call or for a `Watch` the node holds
  with that grid in scope: no walk, glance or feed runs for the process's
  life. The node holds a stream only while something shows the plugin's
  grids, so this is "nothing is done for nobody" one hop further (2026-10-02,
  `docs/plugin-standard.md` rule 8).
- The connection cache walks a far node's whole source on every subscribe
  and again when the connection comes back, so a far home reads while its
  connection is down. It is the decided exception to "nothing is done for
  nobody": the store needs the result, because offline readability means
  everything on the far machine, not only what was visited
  (`sourcecache.kickPrefetch`, 2026-10-06).
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
  a connection's far home are places and get a row; a plugin's grids
  are the swatches, one each, and the plugin has none of its own
  (`client/door.PlacesOf` enumerates, `client/menu.Doors` composes). A
  declared grid is named most specific first, then its instance — "Feed
  (hey)", `door.GridName`, uniformly, with no privileged one, because a
  clipped swatch must still tell them apart (2026-09-30) — and it carries
  its framing, so a plugin's grid reopens where it was left. That row is an open set, so it is
  folded: a menu opens on the primitives with a chevron strip for the section
  above it, and every opening starts folded — the fold is the menu's live
  state and dies with it (`client/menu` decides what a state shows and owns
  the flag).
- Agents reach the node through the MCP API (2026-10-15): `/mcp` on the
  web API, Streamable HTTP, the web password as a bearer beside the cookie,
  and no build gate, since no page is behind it. It decodes onto the one
  router (`internal/mcpapi`): every write reads first and claims what it
  read, every refusal is a tool error with the router's reason, and a tile it
  creates or copies with no cell named lands at the first free cell
  (`store.FreeSlot`). It
  exposes no destroy from the trash, no shells and no config;
  `docs/mcp.md` is the contract. It has ten tools (2026-10-18): `sessions`,
  `read_grid`, `read_tiles`, `find`, `write_text`, `rename`, `move`, `copy`,
  `delete` and `select`, and every write takes a list and answers a verdict
  per item in order, each item its own write with its own claim, a refused
  one stopping none after it. A session answers only where the user is
  (2026-10-18): `sessions` lists every screen showing the node, newest
  change first, each with its workspace tiles and its panes' places, grids and
  focus, and marks yours the one holding the agent's shell live; it chooses
  none, and no screen is an empty list. What a grid holds and has selected
  is the grid's, from `read_grid`, so "here" is a focused pane's grid or
  workspace tile and "these" is that grid's selection; `read_tiles` on a pane
  tile answers the panes its layout holds, decoded by the client's own
  decoder (`pane.DecodeLayout`). A new tmux carries
  its key in `GRIDWELL_SHELL`, Claude Code sends it as `X-Gridwell-Shell`,
  and the shell API, the one place a socket and its session are both in
  hand, joins the two (`Server.ShellHolder`).
- A plugin or connection the user disables, from the Disable button on its
  health notice, stays disabled until the node restarts. It is held in the
  node's memory only (`plugin.Registry.Disable`), never in `server.yaml` or
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
- Plugins serve web content through `/content/<token>/<tile-id>/<subpath>`.
  Every response is sandboxed and gated by the content token, which is never
  interchangeable with the cookie. It is either a text tile or a url tile: a
  text tile cannot serve from the plugin, so `serves_page` rides a url entry
  and the content API refuses it on every other kind.

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
  selection, which is a node fact), the level stack, and, outside a pane
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
  drop lands in or across one. Cross-plugin a left-drag links too, because
  there is no cross-plugin move. Dashed means link; deleting a link
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
- A link is a path, and a link broken at ANY hop — a namespace this node
  does not declare, one a node further along the chain does not declare, or
  a target its namespace says is gone — is DEAD: greyed, inert, still
  labelled, still deletable. It is a state, not an error — nothing is asked
  for it and nothing is said about it (`gwerr.DeadRef` is the answer,
  `client/deadref` the verdict). A gone key listed again is live again under
  the same id. Dead is not dark: a declared
  plugin that is down and a declared connection that will not answer are
  health, and they come back. Nor is dead always forever: a retired
  connection name never returns, but a namespace merely undeclared is live
  again the moment it is declared again. `client/deadref`.
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
  pressed; a live url view's native menu names its pane and focus follows
  before any item runs.
- A left-drag on a pane boundary resizes it. A press grabs at most one
  divider per axis, so at the corner of three panes it grabs both and the
  one drag moves both: two ordinary resizes sharing one gesture, rather than
  a gesture of its own. `pane.GrabDividers`.
- A right-drag from a pane's edge splits it. Inside a workspace tile the outline
  at the window edge is part of the pane it borders, so an edge is as easy
  a target as an interior divider (2026-10-09, `pane.GutterPane`).
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
- No parameterized plugins. Modals center on the active pane.
- A client the node served runs the node's build: the web API refuses any call
  from another build, and the client shows one persistent notice with a
  Reload button and reloads only when the user asks (2026-10-07).

## Gates

`make check` must be green on every commit. It cannot see the native layer,
and that is where the worst bugs live.

| Gate | Sees | Run it when |
|---|---|---|
| `make check` | Go + TS logic; compiles wasm, executes none of it | every commit |
| `make check-electron` | the `WebContentsView` bridge under xvfb | the live url path, `webviews.ts`, the preload |
| `make check-e2e` | the full app as a black box | any `apps/desktop` change, anything under `client/wasm` or `web/`, the native layer, cross-seam behavior |
| `make check-web` | the browser-mode client: caps, touch, shells | `client/caps`, `client/touchgest`, `client/wasm/touch.go`, the browser-serving path and its shell API |
| `make check-connections` | the real binaries through a real ssh tunnel | plugin spawn, the dialer, the export, id routing; a dependency or toolchain bump — grpc, x/net, or the go line is every seam at once, so run it locally before pushing, plus the native gates for a desktop (npm) dependency |

A feature is not done when its gates are green. It is done when a trace of
ordinary use (the Dump logs row) has been read and every refused call,
repeated ask and odd id in it is either fixed or a decision written here.
The 2026-09-27 trace found four defects nine assessments had missed.

If a change touches the native layer or the shim, `make check` passing means
nothing. Run the electron or e2e gate and add a spec. The gates rebuild at
start, and `check-e2e` and `check-web` then test the artifacts they copied at
that moment (`apps/desktop/e2e/runtree.ts`), so a rebuild beside a running one
cannot change what it tests; the other gates have no such pin, so still do
not edit sources while one runs. A flake rerun only counts against a freshly
built tree. A spec on `docs/flake-ledger.md` still gets a fresh look before
you blame a change.

A dependency or toolchain bump commits under the `deps:` prefix. A `deps:`
bump or a shim change names the gates it ran in its message: the bump touches
every seam at once, and nothing in `make check` executes the shim, so in both
the green a commit earns is only worth the gates behind it.

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
- [ ] No import of a plugin implementation anywhere, tests included, and no
      switch on a plugin kind.
- [ ] This is one logical change, and the comments in the files I touched
      are true and meet the comment standard.
- [ ] Things stay as the user left them.
