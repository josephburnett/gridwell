# The anatomy of an id

An id is a chain of segments joined by `/`, read left to right. Each hop
takes one segment; the segment's shape says what it is (`rpc.ShapeOf`):

| Shape | Example | Means |
|---|---|---|
| letter-leading | `ngkwanw`, `fa21d5d1…` | a namespace: a node or a connection (7-char base36 or legacy 32-hex for a node) |
| digits | `14` | a minted row (tile or grid) in home's store: permanent, never reused |
| `~` + base64url(path) | `~L2hvbWUvam9l` | a file or a directory, named by its absolute path: its one public id |

## Examples

```
ngkwanw/14                          home tile 14 (node id ngkwanw)
ngkwanw/~L2hvbWUvam9l               the directory /home/joe, or its door
ngkwanw/~                           the files' owner id (rpc.FilesOwner)
ngkwanw/geneva/rnode1/3             via connection geneva, the far node's tile 3
ngkwanw/geneva/rnode1/~L2V0Yw       a directory of the far node's files
```

## Routing

`Server.resolve` reads one owner rule, `rpc.OwnerNamespaceOf`: under the
node's own id, digits are home, a `~` segment is the files (owner
`<node>/~`), and a letter segment is that connection, which keeps the rest
of the chain for the far node. An id whose first segment is not the node's
own is undeclared and reads dead (`gwerr.DeadLink`). `~` and digits are
always tiles or grids, never namespaces.

The files' owner id `<node>/~` is the key form of the empty path. It routes,
names the files' roster row and keys their health event and a skipped hop
of a find; the files' own ids are qualified with the node's id
(`rpc.QualifierOf`), so a path's id is `<node>/~<path>`, never
`<node>/~/~<path>`.

## Where each appears

- URL path: leading letter segments are the namespace chain, then
  door and tile segments (`~` counts as a tile segment, never a namespace).
- `child_grid_id` (a door or link's target grid) and `link_target_id` (a
  leaf link's target): full chains. A directory's grid and the door that
  shows it in its parent both name the directory's path; `GetGrid` and
  `GetTile` are separate verbs, so the two never collide.
- `Link` (dashed = link) is derived from the target arriving already
  qualified, never stored. A link inside one namespace is still a link: a
  ctrl + right-drag makes one. A symlink's door arrives with the bit set by
  the files, since its child grid is the target's path and not its own.

## What is inside a `~`

The payload is a file's absolute path, base64url without padding
(`rpc.KeyTileID`, `rpc.TileKey`). Only the files open it (`rpc.FilePath`);
to the URL grammar, the router, the content API and the client a `~`
segment is one opaque tile segment. A path is canonical only as spelled:
a root reached through a symlink and its real path are two ids.

## Stability

Digits are never reused. A `~` id is as stable as the path: a file renamed
or moved on disk retires its old id, and a link to it reads dead, since a
link means the thing at that address.

A file keeps its `~` name for good. The first durable fact the user makes
about it (a placement, a framing, a typed name, a freeze) mints a row in
the files' side of the store, keyed by the path, and that row is where the
fact and the tombstone live, but it never becomes a name: the listing
answers the path, a write answers the path, and `child_grid_id` and
`link_target_id` hold it verbatim. A client stands on these ids: a pane
holds its anchor grid id and its content tile id, and the URL projects
both, so a name that changed under the user's hand would leave the pane
naming nothing.

A path the listing no longer names is swept and its row tombstoned; listed
again, it is the same id with a fresh row, holding none of the arrangement
the retired one held.

A connection name in `retired_names:` never returns; a name the config merely
stopped declaring is not retired, and resolves again the moment it is
declared again.
