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
| **file and directory** | Not tiles but the + menu's two red swatches on a writable directory's grid (`menu.Offer.Disk`): each asks for a name in the entry modal, then makes an empty file or a directory on disk, which the listing shows. Every other primitive on a directory's grid makes a Gridwell tile there. |

## Link shapes

| Concept | What it does |
|---|---|
| **door to another namespace** | A door onto a grid in another source — a far home, a directory of the files. Derived from the ids alone (`rpc.IsDoorToOtherNamespace`), never stored. |
| **leaf link** | One content tile shown in two places: `link_target_id` plus one owning row. The same document shown elsewhere, without a copy. |
| **dead link** | A link whose path ends in nothing: a namespace some hop no longer declares, or a target its source says is gone. Grey, inert, no notice, still deletable. Separates "gone" from "not answering". |
| **reference** | A Gridwell URL naming a tile or a grid, its place under the origin the client reached the node by. A browser opens the place; a read with the web password answers its content (`docs/mcp.md`). A drop of a Gridwell tile onto a shell pastes its reference, and a file's drop its absolute path. A reference is not a tile, so nothing stores it. |
| **childless link** | A link that names no namespace yet — the + menu swatch and the drag ghost, drawn before there is a grid to point at. |

## Places

| Concept | What it does |
|---|---|
| **a root** | One directory `files:` declares, a + menu swatch (`door.PlacesOf`). The files have no place of their own, so a root is the menu's way in, and it is named label first, then its source — "Docs (files)" (`door.GridName`). It is a starting point, not a boundary: a symlink, a link or a place URL reaches any path on the host, and one that exists is live. |
| **menu fold** | The + menu's strip, the templates and the swatches, is an open set, so the menu opens with it folded behind the chevron band over the primitives, and every opening starts folded. The fold is the menu's live state, and what a folded or open menu shows is decided in the same package (`client/menu`). |
| **strip** | The unfolded section above the primitives: the copy swatch and the templates newest first, then the swatches, each section wrapped at five to a row and never sharing one (`menu.Layout`). Three rows show; the wheel over the menu, or a drag between its items, scrolls the rest, and every opening starts at the top. The templates are this node's on every menu, a far node's included. |
| **template** | A tile on home's templates grid, drawn in the strip as its crumb is in the bar: its preview filling the square, its kind's border on it. A click visits an ephemeral copy of it, made first, so the template is never touched; a drag out is its tile's drag: a left-drag moves it out of the strip, a right-drag copies, a drop on a shell pastes its reference, the trashcan deletes it. A copy of a door or a workspace tile is retired as a content visit is, when its frame or its level is left. |
| **copy swatch** | The strip's first item, dashed grey with a plus. Dragged onto a tile (on a grid, the one a pane is descended into, an ephemeral visit's, a shell's) it copies that tile into the templates by the copy rules any copy follows, landing above every template; released anywhere else it snaps back. A click on it does nothing. |
| **frame** | One step of a place through a door (`pane.Frame`), into a grid or into a content tile, with where you left it. The whole of where a pane is, and a descent into a document is the same step as a descent into a grid, ascended out of the same way. Text scroll, text mode and content zoom belong to the tile's row, written by `SetTile`; the frame carries the working copy while you are inside it, as it carries the viewport `SetFraming` writes back to the door's row. Inside a workspace tile that viewport and the live text scroll are the pane's own, kept in the layout blob: a settle there writes the row only the text mode, and leaving the text writes the row's whole scroll. |
| **level** | A descent into a workspace tile (`pane.Level`), session-only: a frame moves one pane, a level parks the window's whole pane tree. One workspace tile can hold a whole arrangement without the frame stack encoding a tree. |
| **zoomed pane** | One leaf owns the whole window while the split ratios beneath it stay untouched (`pane.Tree.Zoomed`, the ⛶ title, a click on the title). Session-only outside a workspace tile; inside one it is part of the layout the tile keeps, with the focused pane (`pane.LayoutFingerprint`). A structural edit unzooms first. |

## Lifecycle spaces

| Concept | What it does |
|---|---|
| **ephemeral scratch visit** | A real row in the scratch grid that no pane owns — a url typed into the + menu, a shell opened from it. Try it without placing it: grey border; its crumb in the bar drags as the tile it is, so a left-drag moves it onto a grid and keeps it, the pane following. It writes, freezes, zooms and renames like a placed tile of its kind; the ascent deletes it unless another pane or a workspace tile's layout still holds it, and `gridwell clean` removes one no layout holds. `client/scratch` reads the `Grid.scratch_grid_id` stamp and nothing else. |
| **trash** | Delete moves a tile into a per-month subgrid; delete again destroys it. Ids and versions continue, so links keep resolving, and undo needs no undo machinery. Scratch bypasses it (`deleteBypassesTrash`) — a visit was never placed. A file bypasses it too: its delete moves the file to the OS trash, and a path is never a row of home's. A directory's delete sends the directory to the OS trash and every Gridwell tile over it, or over a directory under it, to this trash (`store.FileRows.TrashOver`); restoring one does not restore the other. |

## Health

| Concept | What it does |
|---|---|
| **dead** (`client/deadlink`) | The namespace is not declared, or the source answered that the target is gone (`gwerr.DeadLink`). Nothing is said, and nothing is asked again until the declaration or that namespace's listing changes, so neither can storm the notice row. |
| **dark** (`internal/sourcecache` and `internal/files`, health events) | Declared, not answering right now — a connection that will not dial, or a root or directory that cannot be read right now. A directory the OS refuses this user is not dark: its grid answers the refusal, and once a read of it was refused its door carries the reason in `read_only`. Fetched, reported, recovers on its own. It is one fact: a grid served by a dark source is served from what the node holds (a connection's cache, a directory's rows), not answered live, and the bar's chip is that fact drawn (`client/cache.SourceDark`). One bar chip; it never moves or restyles a tile. |
| **gone** (`Tile.gone`, `files.goneDoors`) | A directory not on disk that Gridwell tiles are still over, or over a directory under it. Its parent lists it from the rows as a grey door, the muted family, and its grid answers those tiles alone, not writable, so no Gridwell data is lost to a change of the disk; with no tile left it reads dead, and the path made again is an ordinary directory under the same id. |
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
| **moves** (`Grid.moves`) | A tile of this grid moves to another grid, so a left-drag there is a move; where either grid does not, the left-drag is refused before the RPC. Every home grid does, and so does every directory: a Gridwell tile moves between home and a directory as between two home grids, and a file moves to another directory by a rename on disk. A move to another node is refused (`rpc.MoveRefusal`). |
| **directory** (`cache.Grid.Directory`, `rpc.IsFilesID`) | The grid is one of the node's directories, told by its id, so it and every file and directory in it wear the outside colour; a Gridwell tile over it wears its kind's, as in home (`pane.TileFamily`). |
| **serves_page** | This url tile is a page file (an image, video, audio or PDF file) and opens at the `/content/` API, where the node streams the file, instead of at an address of its own. Only a url tile carries it. It has no address to follow the user, and its title and history are the file's, so the freeze writes the screenshot alone (`urlview.Writeback`); the preview, the standing freeze and the zoom are a url tile's. When the file changes on disk, a live view showing it loads it again once on screen, and an unfrozen screenshot gives way to the file's own picture until the next capture (`urlview.PageMoved`, `files.pagePreviewStale`). |
| **text_presentation: plain** | Verbatim preformatted text; no rendered/raw toggle. |
| **text_presentation: both** | The tile is a document: rendered by default, with the toggle to the raw source, whether or not the tile is writable. |
| **text_presentation: info** | The file's name renders as nothing, so the tile has no body: its preview is its size and its extension's type, and a descent shows its file information, with see more opening its bytes in the hex view (`textedit.DecideDescent`, `client/fileinfo`). Any other file reaches the same view by the i button. |
| **freeze** (`Tile.url_frozen`) | A standing user intent not to go live, on a url or a shell alike. The preview is what a frozen tile looks like; the intent is why it stays that way. Freezing is a screenshot, not a kill: the address still resolves, the tmux still runs, and a copy taken while frozen keeps that preview. The reconnect gesture is the one thing that clears it. The field name is from before a shell could be frozen. |
| **content zoom** (`Tile.content_zoom`) | The scale of what renders inside one text, shell or url tile, stored on its row by `SetTile`'s content-zoom arm, so a document reads larger without the grid moving and stays that size wherever the tile is shown. Pane zoom is the grid's viewport; this is the tile's. `client/contentzoom` owns the chord and the range. |
| **theme** (`client/theme`) | Which palette the client draws in, dark or light. A view preference of this browser or this app, not a fact about your things: it is never written, never shared between your machines, and defaults to dark. Right-click the + menu circle on any grid to pick one. |
| **glyph** | The icon a namespace wears on its swatch, its ghost and the crumb of the grid it roots (`door.GlyphFor`): the folder for the files and their directories, the globe for a connection, the door for home, the trash for home's trashcan. An unknown name degrades to the globe. |

The files say whether a text file is a document, plain, or neither, by its
name alone, so a listing reads no file; the user can always see the source
bytes, in the hex view when nothing else shows them. Nothing renders a body
with no way back to its bytes.

`client/textedit` owns the first two presentation rules (`ToggleVisible`,
`PresentationHTML`), so `make check` executes them.
