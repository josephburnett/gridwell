# Concepts

Every distinction a user can see or feel, and what each one does that no
other one does. A concept that cannot fill its second column does not belong
here.

## Content primitives

| Concept | What it does |
|---|---|
| **door** | A tile that leads to a grid: its own, or, as a link, another's. The only tile with a child grid. |
| **text** | The only tile the user types into, and the only one whose `version` claims bytes. |
| **url** | The only tile with an address of its own and the browser's cookies and storage behind it. |
| **shell** | The only tile whose content is a host process. It survives ascent as a tmux. |
| **workspace tile** | The only tile whose content is an arrangement: a pane tree, with each frame's framing and scroll. Descent swaps the window's pane tree, not one pane. |

## Link shapes

| Concept | What it does |
|---|---|
| **door to another namespace** | A door onto a grid in another source — a far home, a directory of the files. Derived from the ids alone (`rpc.IsDoorToOtherNamespace`), never stored. |
| **leaf link** | One content tile shown in two places: `link_target_id` plus one owning row. The same document shown elsewhere, without a copy. |
| **dead link** | A link whose path ends in nothing: a namespace some hop no longer declares, or a target its source says is gone. Grey, inert, no notice, still deletable. Separates "gone" from "not answering". |
| **childless link** | A link that names no namespace yet — the + menu swatch and the drag ghost, drawn before there is a grid to point at. |

## Places

| Concept | What it does |
|---|---|
| **a root** | One directory `files:` declares, a + menu swatch (`door.PlacesOf`). The files have no place of their own, so a root is the only way in, and it is named label first, then its source — "Docs (files)" (`door.GridName`). |
| **menu fold** | The + menu's swatches are an open set, so the menu opens with them folded behind the chevron strip over the primitives, and every opening starts folded. The fold is the menu's live state, and what a folded or open menu shows is decided in the same package (`client/menu`). |
| **frame** | One step of a place through a door (`pane.Frame`), into a grid or into a content tile, with where you left it. The whole of where a pane is, and a descent into a document is the same step as a descent into a grid, ascended out of the same way. Text scroll, text mode and content zoom belong to the tile's row, written by `SetTile`; the frame carries the working copy while you are inside it, as it carries the viewport `SetFraming` writes back to the door's row. Inside a workspace tile that viewport and the live text scroll are the pane's own, kept in the layout blob: a settle there writes the row only the text mode, and leaving the text writes the row's whole scroll. |
| **level** | A descent into a workspace tile (`pane.Level`), session-only: a frame moves one pane, a level parks the window's whole pane tree. One workspace tile can hold a whole arrangement without the frame stack encoding a tree. |
| **zoomed pane** | One leaf owns the whole window while the split ratios beneath it stay untouched (`pane.Tree.Zoomed`, the ⛶ title, a click on the title). Session-only outside a workspace tile; inside one it is part of the layout the tile keeps, with the focused pane (`pane.LayoutFingerprint`). A structural edit unzooms first. |

## Lifecycle spaces

| Concept | What it does |
|---|---|
| **ephemeral scratch visit** | A real row in the scratch grid that no pane owns — a url typed into the + menu, a shell opened from it. Try it without placing it: grey border, promotable onto a grid. It writes, freezes, zooms and renames like a placed tile of its kind; the ascent deletes it unless another pane or a workspace tile's layout still holds it. `client/scratch` reads the `Grid.scratch_grid_id` stamp and nothing else. |
| **trash** | Delete moves a tile into a per-month subgrid; delete again destroys it. Ids and versions continue, so links keep resolving, and undo needs no undo machinery. Scratch bypasses it (`deleteBypassesTrash`) — a visit was never placed. A file bypasses it too: its delete moves the file to the OS trash, and a path is never a row of home's. |

## Health

| Concept | What it does |
|---|---|
| **dead** (`client/deadlink`) | The namespace is not declared, or the source answered that the target is gone (`gwerr.DeadLink`). Nothing is said, and nothing is asked again until the declaration or that namespace's listing changes, so neither can storm the notice row. |
| **dark** (`internal/sourcecache` and `internal/files`, health events) | Declared, not answering right now — a connection that will not dial, or a root or directory that will not read. Fetched, reported, recovers on its own. It is one fact: a grid served by a dark source is served from what the node holds (a connection's cache, a directory's rows), not answered live, and the bar's chip is that fact drawn (`client/cache.SourceDark`). One bar chip; it never moves or restyles a tile. |
| **disabled** (`connection.Server.Disable`, `EventSourceHealth.disabled`) | A connection the user switched off from its health notice, until the node restarts. It reads exactly as dark — the cache where there is one, the read failing where there is none — but nothing tries it again and no notice stands, and it is read-only: a write is refused (`gwerr.SourceDisabled`) and dropped, never retried, and its rows say why (`Tile.read_only`). Held in the node's memory only: `server.yaml` and `gridwell.db` never record it, so a restart brings the connection back. The files are never disabled. |
| **waiting** (`sourcehealth.Waiting`) | A connection row minted with no root and no error: asked, not answered yet. The click reports at `Info`, and the probe's timeout ends the wait. |
| **broken** (`sourcehealth.Broken`) | A door that will not open, whatever the reason — `Info` failed, the probe timed out. That is exactly `InfoError` being set. One tint; the click reports at `Error` and `BrokenReason` carries the detail. |
| **unknown** | Not yet known, which is neither yes nor no. `scratch.For`, `a.gridWritable` and `a.gridAcceptsTiles` all return `(value, known)`, so each caller picks its own safe default where the reason is visible. |

`sourcehealth.Classify` answers only for a row that can be a door: a node's
home, a connection, a root's swatch. The files' own row is none of those,
so it has no status at all — nothing draws it, and no click reaches it.

## Write classes

| Concept | What it does |
|---|---|
| **content** | The user's bytes: a text body, a typed url, a typed name, a workspace tile's layout. The only class that claims a version and can 409. A file's body has no version, so its write claims the stamp of the bytes it was typed over (`Tile.content_stamp`, `<mtime_ns>-<size>`), and a stale stamp is the same conflict. A file's url tile is a page file, which has no address of its own, so a navigation in it writes nothing. |
| **framing** | Where you left a view: `SetFraming`, and `SetTile`'s text-scroll and content-zoom arms. No claim, no bump. The standing freeze (`SetTile`'s url-frozen arm) bumps nothing but claims the freeze it saw. |
| **capture** | What the machine observed: a preview JPEG, a page title, a url trail. No claim, no bump. |
| **placement** | Where things sit: move, copy, delete. A move claims the placement it saw (grid and footprint), not a version, and bumps none; a delete claims the version and the placement. A stale one is refused and the tile stays where it is. |
| **selection** (`Grid.selection`) | The set of a grid's tiles the user has in hand, kept on the grid's row by the node and persisted. A select names the tiles it adds and removes, claims nothing and bumps no version, so the user's and an agent's selects of different tiles both stand. A tile that leaves the grid leaves the set. It is not a pane's focused tile, the keyboard target a plain click moves, which is the session's. Shift + left-click a tile to toggle it; shift + left-drag on the background sweeps a band of cells and adds every tile wholly inside it (one it only touches stays out); Esc with no drag in flight clears it; Clear selections in the + circle's right-click menu clears every grid's; on touch, a long-press lifted without moving toggles. Shift is the only select key. Plain clicks, descents and typing never change it. Every selected tile wears a yellow border in every open view. A move, copy, link or delete gesture on a selected tile acts on the whole set (`dragdrop.CarryOf`). Inside a workspace tile it is the grid's too: every pane showing the grid shows the one set. |

The selection is a grid row's, with its own writer (`setSelectionTx`). For
the tile row the store enforces two of the other four, not four: `claimContentVersion` +
`finishContentEdit` for content, `loadForWrite` + `emitTileChanged` for the
other three. Framing, capture, and placement are three origins of one enforced
class, and the word says who started the write.

## Presentation exceptions

| Concept | What it does |
|---|---|
| **read-only** (`a.tileReadOnly`) | A text tile in a grid that is not writable (`Grid.writable`, its bodies take edits): no textarea, no save, no checkbox flip, rendered view only. Nobody types into a derived body and silently re-posts it. A home grid is writable, and so is a directory, whose text files save to disk like a home document; a file the node cannot write is read-only with the reason (`Tile.read_only`). |
| **accepts tiles** (`Grid.accepts_tiles`) | The grid takes new tiles: the + menu offers the primitives, a swatch or a copy drops into it, and its url rows' titles and trails are the node's to write. Every home grid and every directory does: a Gridwell tile on a directory is home's row over it, and a file or a directory is made on disk only by the file and directory primitives (`CreateTileRequest.file`). That is apart from whether its bodies take edits. |
| **moves** (`Grid.moves`) | A tile of this grid moves to another grid of its namespace, so a left-drag there is a move; where either grid does not, the left-drag is refused before the RPC. Every home grid does, and so does every directory: a file moves to another directory by a rename on disk. |
| **directory** (`cache.Grid.Directory`, `rpc.IsFilesID`) | The grid is one of the node's directories, told by its id, so it and every row in it get the outside treatment. |
| **serves_page** | This url tile is a page file (an image, video, audio or PDF file) and opens at the `/content/` API, where the node streams the file, instead of at an address of its own. Only a url tile carries it. It has no address to follow the user, and its title and history are the file's, so the freeze writes the screenshot alone (`urlview.Writeback`); the preview, the standing freeze and the zoom are a url tile's. When the file changes on disk, a live view showing it loads it again once on screen, and an unfrozen screenshot gives way to the file's own picture until the next capture (`urlview.PageMoved`, `files.pagePreviewStale`). |
| **text_presentation: plain** | Verbatim preformatted text; no rendered/raw toggle. |
| **text_presentation: both** | The tile is a document: rendered by default, with the toggle to the raw source, whether or not the tile is writable. |
| **freeze** (`Tile.url_frozen`) | A standing user intent not to go live, on a url or a shell alike. The preview is what a frozen tile looks like; the intent is why it stays that way. Freezing is a screenshot, not a kill: the address still resolves, the tmux still runs, and a copy taken while frozen keeps that preview. The reconnect gesture is the one thing that clears it. The field name is from before a shell could be frozen. |
| **content zoom** (`Tile.content_zoom`) | The scale of what renders inside one text, shell or url tile, stored on its row by `SetTile`'s content-zoom arm, so a document reads larger without the grid moving and stays that size wherever the tile is shown. Pane zoom is the grid's viewport; this is the tile's. `client/contentzoom` owns the chord and the range. |
| **theme** (`client/theme`) | Which palette the client draws in, dark or light. A view preference of this browser or this app, not a fact about your things: it is never written, never shared between your machines, and defaults to dark. Right-click the + menu circle on any grid to pick one. |
| **glyph** | The icon a namespace wears on its swatch, its ghost and the crumb of the grid it roots (`door.GlyphFor`): the folder for the files and their directories, the globe for a connection, the door for home, the trash for home's trashcan. An unknown name degrades to the globe. |

The files say whether a text file is a document, by its extension; the user
can always see the source bytes. That is why there are two values and not
three: nothing renders a body with no way back to its bytes.

`client/textedit` owns the first two presentation rules (`ToggleVisible`,
`PresentationHTML`), so `make check` executes them.
