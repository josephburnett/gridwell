# The MCP API

An AI agent such as Claude Code reaches your Gridwell node through the MCP
API. MCP is the Model Context Protocol: the agent lists the API's tools and
calls them. The API does what the app does when you make the same gesture,
and refuses what the app refuses, with the same reason. This page is the
contract: the tools, what each does, and what each refuses.

## Connecting

The API is `/mcp` on the web API (`web.bind` in `server.yaml`), over
Streamable HTTP. It takes the web password as a bearer token:

```
Authorization: Bearer <the contents of ~/.gridwell/web-password>
```

The browser's login cookie works too. `gridwell serve` prints the line that
registers the API with Claude Code:

```
claude mcp add --transport http gridwell http://127.0.0.1:<port>/mcp -H "Authorization: Bearer <password>" -H 'X-Gridwell-Shell: ${GRIDWELL_SHELL:-}'
```

The second header tells the API which shell tile the agent runs in (see
Here and these). Keep it in single quotes: Claude Code, not the shell you paste the
line into, fills in `${GRIDWELL_SHELL:-}` each time it starts. Inside a shell
tile it is that shell's name; in any other terminal it is empty.

Delete the `web-password` file to rotate the password; the registration then
needs the new one.

## Addresses

Every tile and grid has an id, a path such as `k3x9a2b/12` (a tile at home)
or `k3x9a2b/laptop/m4q8z1c/7` (a tile on a far node, through the connection
named `laptop`). Every answer names tiles by that id. Pass it back exactly.
Grids and tiles are numbered apart, so a grid and a tile may share a
spelling; each argument says which it takes.

## Claims

A write claims what the agent read, as the app does. `read_tiles` and
`read_grid` answer a `version` (and, for a text body, `body_version`, or
`body_stamp` for a plugin's text). Pass it to a write and a change made since
you read is refused as a conflict; read again and decide. Leave it out and the
API reads the row and claims it as it stands now. A move claims where the
tile sits now; a delete claims its version and where it sits. `select` claims
nothing: it names the tiles it adds and removes, or clears, so the user's own
selects made meanwhile stand beside it.

## Lists and verdicts

Every write takes `items`, a list, and applies them in order. Each item is
its own write with its own claim, and the answer is one verdict per item, in
the same order: `ok` with the tile as the write left it, or the refusal, in
the same form as a refused call (see Refusals). A refused item stops nothing:
the items after it still run. `read_tiles` answers the same way, one answer
per id. A call is refused whole only when it is wrong as a whole, such as an
empty list.

## Tools

| Tool | Arguments | What it does |
|---|---|---|
| `sessions` | none | Every screen showing the node, the most recently changed first (see Here and these): its `id`, `label` (`desktop` or `browser`), when its view last `changed`, `away_since` when its stream has closed, the `workspace_tiles` it is inside, outermost first, and its `panes`: each one's `id`, place (`anchor` and `path`), the `grid` it shows, the `content` tile it is inside, and whether it is `focused`. The screen showing the shell you run in is marked `yours`. No screen is an empty list. |
| `read_grid` | `id`, optional | A grid's facts (`writable`: its text takes edits; `accepts_tiles`: it takes new tiles; `selection`: the tiles the user has in hand) and its rows: id, kind, name, placement, version, and for a link the address it resolves to and whether it is dead. No id reads the node's home grid. |
| `read_tiles` | `ids` | One answer per id, in order: the row, and what its kind holds: a text tile's body with `body_version`, a url's address, the tmux a shell names, the grid a door holds, and for a workspace tile its `layout`: the `version` it was read at and each pane's `id`, place (`anchor` and `path`), the `grid` when the layout names it (a pane with no path is on its anchor), the `content` tile it is inside, and whether it is `focused`, the pane the workspace tile reopens on. A link answers its target's content, or `dead` when the target is gone. An id that cannot be read gets its refusal as its verdict. |
| `find` | `query`, `scope`, `limit`, `grid`, `recurse`, `created`, `modified`, `content_changed`, `selected`, `kind` | Tiles whose name, body or address matches, everywhere this node reaches or within the namespace or id in `scope`. `id:<tile-id>` finds one tile. The filters narrow it and combine: `grid` keeps the tiles on that grid, and with `recurse` the grids inside its doors, to any depth (a link door is not followed); `created`, `modified` (any write: a move, a framing, content) and `content_changed` keep the tiles whose time falls in the range, `{from, to}`, each an RFC 3339 time such as `2026-10-17T09:00:00Z`, inclusive, either left out for open; `selected` keeps the tiles in their grid's selection; `kind` keeps the tiles of one kind, `text`, `url`, `shell`, `door` or `workspace`, and any other is refused. With a filter the query may be empty, and the answer is what passes, newest first, by the time it filters on, else by when each was made. Each hit names the doors it sits inside; `skipped` names every namespace that did not answer. |
| `write_text` | `items`: `id`, `grid`, `body`, `version` or `stamp`, `near` | An item with `id` replaces that text tile's whole body; through a link it writes the target. An item with `grid` and no `id` creates a 1×1 text tile born with the body, its name the body's first line, at the first free cell in reading order, or at `near` (`{x, y}`), or below it in its column when that cell is taken. A new tile claims nothing. |
| `rename` | `items`: `id`, `name`, `version` | Renames a url, shell, door or workspace tile, home's or a plugin's. A plugin's entry keeps the typed name on the node's row, over its source's. |
| `move` | `items`: `id`, `x`, `y`, `w`, `h`, `grid` | Moves or resizes a tile. `w`/`h` of 0 keep its size; `grid` moves it to another grid of the same namespace. |
| `copy` | `items`: `id`, `grid`, `at`, `link` | Lands in `grid` or the tile's own, at `at` or beside the tile. `link: false` is a deep copy; across namespaces a source that does not answer lands as a link. `link: true` is a link to the tile: the same content shown in a second place, drawn dashed. A link to a link names the content. |
| `delete` | `items`: `id`, `version` | Moves a home tile to the trash. A plugin's entry is deleted as its plugin deletes (fs removes the file, gitlab marks the todo done, a read-only one refuses), never trashed. Deleting a link only unlinks. |
| `select` | `grid`, `add`, `remove`, `clear`, `recurse`, `all` | Changes the grid's selection: `clear` empties it, then `add` puts tiles in and `remove` takes them out; a tile not on the grid is ignored. `clear` with `recurse` also empties every grid inside the grid's doors, to any depth, in the same namespace; `recurse` alone is refused. `clear` with `all` and no grid empties every selection the node holds, and each connected far node's. It claims nothing, and the last select of a tile wins. Use it to hand a set back: "I selected the five I changed". Every pane showing the grid shows it, inside a workspace tile too. |

## Here and these

When the user says "here", they mean the grid in the focused pane of their
screen, or the workspace tile they are in. `sessions` lists the screens and says
where each one is; it does not choose one. "These" is that grid's selection,
the `selection` `read_grid` answers: every pane showing a grid shows its one
selection, inside a workspace tile too, and `select` sets it.

Which screen is theirs: the one marked `yours`, which shows the shell you run
in, live, right now. Every shell tile starts with `GRIDWELL_SHELL` set to its
name, and the registration line sends it as the `X-Gridwell-Shell` header.
Only one screen at a time shows a shell live; when another takes it over,
that one is marked. When none is marked, `note` says why: the shell is live
on no screen, or the node could not tell. A shell started before this API
existed has no `GRIDWELL_SHELL` until it is started again. With no screen
marked, the first is the one whose view changed last; ask the user when more
than one could be theirs.

A screen whose stream closed, as a phone's browser does whenever it goes to
the background, is away. It is still listed for ten minutes
(`subscription.AwayFor`) after it went away, with `away_since`.

Only screens showing this node are listed: a screen on another node that
reaches here through a connection is that node's. The workspaces the user
has used lately are workspace tiles: `find` with `kind` `workspace` and
`content_changed` `{}` (both ends open) lists them by when their layout last
changed, newest first.

## Refusals

A refused call is a tool error, and a refused item's verdict is text; each
starts with what kind of no it is, then the node's reason:

- `conflict:` the claim is stale (something changed since you read), or the
  cells are taken. Nothing was written.
- `disabled:` the source is one the user switched off. It is read-only until
  the node restarts.
- `dead link:` the path ends in nothing: a namespace not declared, or a
  target its source says is gone.
- `unreachable:` the plugin or far node did not answer. Try again later.
- `refused:` the request itself is wrong, such as renaming a text tile, whose
  name is its first line.
- `not supported here:` the namespace does not do this, such as creating a
  tile in a plugin's grid.
- `not found:` no such tile or grid.

## Not exposed

By decision, the API has no tool that destroys a tile from the trash,
nothing that touches a shell (opening, typing into, or reading a terminal;
the shell header only says which screen is yours),
and nothing that changes the node's configuration. There is no
configuration for the API itself: it is on whenever the web API is.
