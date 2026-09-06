# Concepts

Every distinction a user can see or feel, and what each one does that no
other one does. A concept that cannot fill its second column does not belong
here.

## Content primitives

| Concept | What it does |
|---|---|
| **well** | The only tile with a child grid, and the only doorway that pushes a grid frame. |
| **text** | The only tile the user types into, and the only one whose `version` claims bytes. |
| **url** | The only tile with an address of its own and a Chromium session behind it. |
| **shell** | The only tile whose content is a host process. It survives ascent as a tmux session. |
| **pane** | The only tile whose content is an arrangement. Descent swaps the window's pane tree, not one pane. |

## Reference shapes

| Concept | What it does |
|---|---|
| **exit well** | A doorway onto a grid in another namespace — a mount, a plugin, a file tree. Derived from the ids alone (`rpc.IsExitWell`), never stored. |
| **leaf link** | One content tile shown in two places: `link_target_id` plus one owning row. The same document, over there, without a copy. |
| **dead link** | A link into a namespace the node no longer declares: grey, inert, no RPC, no notice. Separates "gone from the config" from "not answering". |
| **childless reference** | A link that names no namespace yet — the + menu row and the drag ghost, drawn before there is a grid to point at. |

## Places

| Concept | What it does |
|---|---|
| **grid frame** | One doorway crossing, with the viewport you left it at. The whole of where a pane is. |
| **content frame** | A frame whose place is a tile rather than a grid (`pane.ContentFrame`): text scroll, text mode, and content zoom live here. A descent into a document is an ordinary level you can ascend out of. |
| **level** | A pane-tile descent (`pane.Level`), session-only. One pane tile can hold a whole arrangement without the frame stack encoding a tree. |

## Lifecycle spaces

| Concept | What it does |
|---|---|
| **ephemeral scratch visit** | A real row in the scratch grid that no pane owns — a url typed into the + menu, a shell opened from it. Try it without placing it: grey border, gone on ascent, promotable onto a grid. `client/scratch` reads the `Grid.scratch_grid_id` stamp and nothing else. |
| **trash** | Delete moves a tile into a per-month subgrid; delete again destroys it. Ids and versions continue, so links keep resolving, and undo needs no undo machinery. Scratch bypasses it (`deleteBypassesTrash`) — a visit was never placed. |

## Health and staleness

| Concept | What it does |
|---|---|
| **dead** (`client/deadref`) | The namespace is not declared. Nothing is asked and nothing is said, so an undeclared plugin cannot storm the strip. |
| **dark** (`internal/sourcecache`, health events) | Declared, not answering right now. Fetched, reported, recovers on its own. |
| **stale** (`Grid.Meta.Stale`) | This grid is a memory rather than an answer. One bar chip; it never moves or restyles a tile. |
| **waiting** (`pluginhealth.Waiting`) | A connection row minted with no root and no error: asked, not answered yet. The click reports at `Info`, and the probe's timeout ends the wait. |
| **broken** (`pluginhealth.Broken`) | A launcher that will not open, whatever the reason — `Info` failed, the probe timed out, or `Info` declared no root grid. One tint; the click reports at `Error` and `BrokenReason` carries the detail. |
| **unknown** | Not yet known, which is neither yes nor no. `scratch.For` and `a.gridWritable` both return `(value, known)`, so each caller picks its own safe default where the reason is visible. |

## Write classes

| Concept | What it does |
|---|---|
| **content** | The user's bytes: a text body, a typed url, a typed name. The only class that claims a version and can 409. |
| **framing** | Where you left a view: `SetFraming`, `SetTextView`, `SetContentZoom`. No claim, no bump. |
| **capture** | What the machine observed: a preview JPEG, a page title, a url trail. No claim, no bump. |
| **layout** | Where things sit: place, clone, delete. No claim, no bump; last-writer-wins. |

The store enforces two of these, not four: `claimContentVersion` +
`finishContentEdit` for content, `loadForWrite` + `emitTileChanged` for the
other three. Framing, capture, and layout are three origins of one enforced
class, and the word says who started the write.

## Presentation exceptions

| Concept | What it does |
|---|---|
| **read-only** (`a.tileReadOnly`) | A text tile in a grid that is not writable: no textarea, no save, no checkbox flip, rendered face only. Nobody types into a derived body and silently re-posts it. |
| **host_content** (`Grid.Meta.HostContent`) | Every row in this grid projects host state and gets the red "outside Gridwell" treatment. The plugin declares it, so the client never learns plugin kinds. |
| **serves_page** | This tile's face and descent are web content served at the `/content/` door: a file whose presentation is a page, with no address of its own. A url entry that also declares it is refused at the plugin door (`checkEntries`). |
| **text_presentation: plain** | Verbatim preformatted text; no rendered/raw toggle. |
| **text_presentation: rendered** | Document render only; no toggle. |
| **text_presentation: both** | The toggle stays, whether or not the tile is writable. |
| **url_frozen** | A standing user intent not to go live. A preview is what a frozen tile looks like; the intent is why it stays that way. |

`client/textedit` owns the first two presentation rules (`ToggleVisible`,
`PresentationHTML`), so `make check` executes them.
