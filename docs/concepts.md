# Concepts

Every distinction a user can see or feel, and what each one does that no
other one does. A concept that cannot fill its second column does not belong
here.

## Content primitives

| Concept | What it does |
|---|---|
| **well** | The only tile with a child grid, and the only doorway that pushes a frame onto a grid. |
| **text** | The only tile the user types into, and the only one whose `version` claims bytes. |
| **url** | The only tile with an address of its own and a Chromium session behind it. |
| **shell** | The only tile whose content is a host process. It survives ascent as a tmux session. |
| **pane** | The only tile whose content is an arrangement. Descent swaps the window's pane tree, not one pane. |

## Reference shapes

| Concept | What it does |
|---|---|
| **exit well** | A doorway onto a grid in another namespace — a mount, a plugin, a file tree. Derived from the ids alone (`rpc.IsExitWell`), never stored. |
| **leaf link** | One content tile shown in two places: `link_target_id` plus one owning row. The same document, over there, without a copy. |
| **dead link** | A link whose path ends in nothing: a namespace some hop no longer declares, or a target its namespace says is gone. Grey, inert, no notice, still deletable. Separates "gone" from "not answering". |
| **childless reference** | A link that names no namespace yet — the + menu row and the drag ghost, drawn before there is a grid to point at. |

## Places

| Concept | What it does |
|---|---|
| **collection** | One grid a plugin serves, declared as a + menu entry (`door.PlacesOf`). A plugin has no place of its own, so a collection is the only way in, and it is named label first, then its instance — "Feed (hey)" (`door.EntryName`). |
| **menu fold** | The + menu's doorway row is an open set, so it opens folded behind a chevron over the primitives, and every opening starts folded. The fold is the menu's live state (`client/menu`); what a folded or open menu shows is `client/palette`'s. |
| **frame** | One step through a doorway (`pane.Frame`), into a grid or into a tile, with where you left it. The whole of where a pane is, and a descent into a document is the same step as a descent into a grid, ascended out of the same way. Text scroll, text mode and content zoom belong to the tile's row, written by `SetTile`; the frame carries the working copy while you are inside it, as it carries the viewport `SetFraming` writes back to the doorway's row. |
| **level** | A pane-tile descent (`pane.Level`), session-only: a frame moves one pane, a level parks the window's whole pane tree. One pane tile can hold a whole arrangement without the frame stack encoding a tree. |
| **zoomed pane** | One leaf owns the whole window while the split ratios beneath it stay untouched (`pane.Tree.Zoomed`, the ⛶ title, a click on the title). Session-only; a structural edit unzooms first. |

## Lifecycle spaces

| Concept | What it does |
|---|---|
| **ephemeral scratch visit** | A real row in the scratch grid that no pane owns — a url typed into the + menu, a shell opened from it. Try it without placing it: grey border, gone on ascent, promotable onto a grid. `client/scratch` reads the `Grid.scratch_grid_id` stamp and nothing else. |
| **trash** | Delete moves a tile into a per-month subgrid; delete again destroys it. Ids and versions continue, so links keep resolving, and undo needs no undo machinery. Scratch bypasses it (`deleteBypassesTrash`) — a visit was never placed. |

## Health

| Concept | What it does |
|---|---|
| **dead** (`client/deadref`) | The namespace is not declared, or it answered that the key is gone (`gwerr.DeadRef`). Nothing is said, and nothing is asked again until the declaration or that namespace's listing changes, so neither can storm the strip. |
| **dark** (`internal/sourcecache` and `internal/pluginhost`, health events) | Declared, not answering right now — a connection that will not dial, or a plugin whose directory or API stopped answering while its process still does. Fetched, reported, recovers on its own. It is one fact: a room served by a dark source is a memory rather than an answer, and the bar's chip is that fact drawn (`client/cache.SourceDark`). One bar chip; it never moves or restyles a tile. |
| **waiting** (`pluginhealth.Waiting`) | A connection row minted with no root and no error: asked, not answered yet. The click reports at `Info`, and the probe's timeout ends the wait. |
| **broken** (`pluginhealth.Broken`) | A doorway that will not open, whatever the reason — `Info` failed, the probe timed out. That is exactly `InfoError` being set. One tint; the click reports at `Error` and `BrokenReason` carries the detail. |
| **unknown** | Not yet known, which is neither yes nor no. `scratch.For`, `a.gridWritable` and `a.gridAcceptsTiles` all return `(value, known)`, so each caller picks its own safe default where the reason is visible. |

`pluginhealth.Classify` answers only for a row that can be a door: a node's
home, a connection, a plugin's collection swatch. A plugin's own row is none of
those, so it has no status at all — nothing draws it, and no click reaches it.

## Write classes

| Concept | What it does |
|---|---|
| **content** | The user's bytes: a text body, a typed url, a typed name. The only class that claims a version and can 409. A plugin's body has no version, so its write claims the source's stamp of the bytes it was typed over (`Tile.content_stamp`), and a stale stamp is the same conflict. |
| **framing** | Where you left a view: `SetFraming`, and `SetTile`'s text-window, content-zoom and url-frozen arms. No claim, no bump. |
| **capture** | What the machine observed: a preview JPEG, a page title, a url trail. No claim, no bump. |
| **layout** | Where things sit: place, clone, delete. No claim, no bump; last-writer-wins. |

The store enforces two of these, not four: `claimContentVersion` +
`finishContentEdit` for content, `loadForWrite` + `emitTileChanged` for the
other three. Framing, capture, and layout are three origins of one enforced
class, and the word says who started the write.

## Presentation exceptions

| Concept | What it does |
|---|---|
| **read-only** (`a.tileReadOnly`) | A text tile in a grid that is not writable (`Grid.writable`, its bodies take edits): no textarea, no save, no checkbox flip, rendered face only. Nobody types into a derived body and silently re-posts it. A home grid is writable; a plugin's is exactly when the plugin declares `writable`, and then its text tiles save to the source like a home document. |
| **accepts tiles** (`Grid.accepts_tiles`) | The grid takes new tiles: the + menu offers the primitives, a swatch or a clone drops into it, and its url rows' addresses are the node's to write. Every home grid does; a plugin's never does, since a plugin creates nothing, and that is apart from whether its bodies take edits. |
| **host_content** (`Grid.Meta.HostContent`) | Every row in this grid projects host state and gets the red "outside Gridwell" treatment. The plugin declares it, so the client never learns plugin kinds. |
| **serves_page** | This url tile opens at the `/content/` door, where its plugin serves the page, instead of at an address of its own. It is either a text tile or a url tile: a text tile cannot serve from the plugin, so the plugin door (`acceptEntries`) refuses the declaration on every kind but url. The address, title and history are the plugin's, so the freeze writes the screenshot alone (`urlview.Writeback`); the face, the standing freeze and the zoom are a url tile's. When its plugin says the page changed in place, a live view showing it loads it again once on screen, and an unfrozen screenshot gives way to the plugin's picture until the next capture (`urlview.PageMoved`, `pluginhost.pageFaceStale`). |
| **text_presentation: plain** | Verbatim preformatted text; no rendered/raw toggle. |
| **text_presentation: both** | The tile is a document: rendered by default, with the toggle to the raw source, whether or not the tile is writable. |
| **frozen** (`Tile.url_frozen`) | A standing user intent not to go live, on a url or a shell alike. The preview is what a frozen tile looks like; the intent is why it stays that way. Freezing is a screenshot, not a kill: the address still resolves, the tmux session still runs, and a clone taken while frozen keeps that face. The reconnect gesture is the one thing that clears it. The field name is from before a shell could be frozen. |
| **content zoom** (`Tile.content_zoom`) | The scale of what renders inside one text, shell or url tile, stored on its row by `SetTile`'s content-zoom arm, so a document reads larger without the grid moving and stays that size wherever the tile is shown. Pane zoom is the grid's viewport; this is the tile's. `client/contentzoom` owns the chord and the range. |
| **status_detail** | The owning plugin's one emoji about a tile's state (✅ done, ● unread, ★ starred), set only when there is something to notice and drawn before the name wherever the name is (`tileface.BannerText`). A note on a name, never a second name, and nothing outside the plugin can derive it. |
| **theme** (`client/theme`) | Which palette the client draws in, dark or light. A view preference of this browser or this app, not a fact about your things: it is never written, never shared between your machines, and defaults to dark. Right-click the + menu circle on any grid to pick one. |
| **identity glyph** | The face a namespace wears on its swatch, its ghost and the crumb of the grid it roots (`door.GlyphFor`): declared by the plugin or the entry, globe for a connection, well for home. An unknown name degrades to the globe, so the client learns no plugin kinds. |

A plugin says whether its text is a document; the user can always see the
source. That is why there are two values and not three: a declaration that
rendered a body with no way back to its bytes is retired, and `acceptEntries`
reads the retired word as `both` so an old plugin binary keeps presenting.

`client/textedit` owns the first two presentation rules (`ToggleVisible`,
`PresentationHTML`), so `make check` executes them.
