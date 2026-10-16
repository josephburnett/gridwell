# The MCP door

An AI agent such as Claude Code reaches your Gridwell node through the MCP
door. MCP is the Model Context Protocol: the agent lists the door's tools and
calls them. The door does what the app does when you make the same gesture,
and refuses what the app refuses, with the same reason. This page is the
contract: the tools, what each does, and what each refuses.

## Connecting

The door is `/mcp` on the web door (`web.bind` in `server.yaml`), over
Streamable HTTP. It takes the web password as a bearer token:

```
Authorization: Bearer <the contents of <home>/web-password>
```

The browser's login cookie works too. `gridwell serve` prints the line that
registers the door with Claude Code:

```
claude mcp add --transport http gridwell http://127.0.0.1:<port>/mcp -H "Authorization: Bearer <password>"
```

Delete the `web-password` file to rotate the password; the registration then
needs the new one.

## Addresses

Every tile and grid has an id, a path such as `k3x9a2b/12` (a tile at home)
or `k3x9a2b/laptop/m4q8z1c/7` (a tile on a far node, through the connection
named `laptop`). Every answer names tiles by that id. Pass it back exactly.
Grids and tiles are numbered apart, so a grid and a tile may share a
spelling; each argument says which it takes.

## Claims

A write claims what the agent read, as the app does. `read_tile` and
`read_grid` answer a `version` (and, for a text body, `body_version`, or
`body_stamp` for a plugin's text). Pass it to a write and a change made since
you read is refused as a conflict; read again and decide. Leave it out and the
door reads the row and claims it as it stands now. A move claims where the
tile sits now; a delete claims its version and where it sits.

## Tools

| Tool | Arguments | What it does |
|---|---|---|
| `read_grid` | `id`, optional | A grid's facts (`writable`: its text takes edits; `accepts_tiles`: it takes new tiles) and its rows: id, kind, name, placement, version, and for a link the address it resolves to and whether it is dead. No id reads the node's home grid. |
| `read_tile` | `id` | One row, and what its kind holds: a text tile's body with `body_version`, a url's address, the tmux session a shell names, the grid a well holds. A link answers its target's content, or `dead` when the target is gone. |
| `find` | `query`, `scope`, `limit` | Tiles whose name, body or address matches, everywhere this node reaches or within the namespace or id in `scope`. `id:<tile-id>` finds one tile. Each hit names the wells it sits inside; `skipped` names every namespace that did not answer. |
| `create_text` | `grid`, `body`, `near` | A 1×1 text tile born with the body. Its name is the body's first line. It lands at the first free cell in reading order, or at `near` (`{x, y}`), or below it in its column when that cell is taken. |
| `write_text` | `id`, `body`, `version` or `stamp` | Replaces a text tile's whole body. Through a link it writes the target. |
| `rename` | `id`, `name`, `version` | Renames a url, shell, well or pane tile. |
| `place` | `id`, `x`, `y`, `w`, `h`, `grid` | Moves or resizes a tile. `w`/`h` of 0 keep its size; `grid` moves it to another grid of the same namespace. |
| `clone` | `id`, `grid`, `at` | A deep copy, in `grid` or the tile's own, at `at` or beside the tile. Across namespaces a source that does not answer lands as a link. |
| `link` | `id`, `grid`, `at` | A link to the tile: the same content shown in a second place, drawn dashed. A link to a link names the content. |
| `delete` | `id`, `version` | Moves the tile to the trash. Deleting a link only unlinks. |

## Refusals

A refused call is a tool error whose text starts with what kind of no it is,
then the node's reason:

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
  tile in a plugin's collection.
- `not found:` no such tile or grid.

## Not exposed

By decision, the door has no tool that destroys a tile from the trash,
nothing that touches a shell (opening, typing into, or reading a terminal),
and nothing that changes the node's configuration. There is no
configuration for the door itself: it is on whenever the web door is.
