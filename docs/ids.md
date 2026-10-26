# The anatomy of an id

An id is a chain of segments joined by `/`, read left to right. Each hop
takes one segment; the segment's shape says what it is:

| Shape | Example | Means |
|---|---|---|
| letter-leading | `ngkwanw`, `fa21d5d1…` | a namespace: node, plugin, or connection (7-char base36 or legacy 32-hex) |
| digits | `14` | a minted row (tile or grid) in the owner's store — permanent, never reused |
| `~` + base64url(address) | `~L2hvbWUvam9l` | a plugin thing named by its own address — its one public id, tile or grid |

## Examples

```
52f8374f…/14                    home tile 14 (node id = home's id)
ngkwanw/7                       a gitlab tile stored under row 7 before this rule
fa21d5d1…/~L2hvbWUvam9l         the fs grid of /home/joe
8aed…/eoifgyl/rp1nodeX/3        via connection eoifgyl → remote node → its tile 3
```

## Routing

`Server.resolve`, per hop: own-node id + digits → home; own-node id +
letter segment → that connection; any other letter segment → that plugin.
`~` and digits are always leaves-or-rows, never namespaces.

## Where each appears

- URL path: leading letter segments = the namespace chain, then
  door/tile segments (`~` counts as a tile segment, never a namespace).
- `child_grid_id` (a door or link's target grid) and `link_target_id` (a
  leaf link's target): full chains. A key names the entry; whether it
  stands for the entry's tile or its grid is positional, same as digits.
- `Link` (dashed = link) is derived from the target arriving already
  qualified — never stored. A link inside one namespace is still a
  link: a ctrl + right-drag makes one, and so does a same-namespace
  link door. A uuid comparison would miss both.

## What is inside a `~`

The payload is the owning namespace's own address for the thing, and no one
else opens it: to the URL grammar, the router, the /content/ API and the
client, a `~` segment is one opaque tile segment. `internal/pluginhost` is the
only reader, and it writes two forms (`address.go`):

| Position | Payload | Example |
|---|---|---|
| grid | the plugin's grid key (its name for good) | `~` + b64(`/home/joe`) |
| tile | the grid key, NUL, the entry key | `~` + b64(`/home` NUL `/home/joe`) |

A tile carries its grid because a tile must be answerable on its own. The
node keeps no key→grid index, because such an index would be exactly the
row lazy minting exists to avoid, and `plugin.v1` has no verb that describes
one entry — so `GetTile` on an entry with no row is one `List` of the grid
that names it.

## Stability

Digits are never reused. A `~` id is as stable as the plugin's key (keys
are forever, per the plugin contract).

A plugin thing — tile or grid — keeps its `~` name for good. The first
durable fact the user makes about it mints a row in the plugin's namespace
of the store, and that row is where the placement, the framing and the
tombstone live, but it never becomes a name: the listing answers the
address, a write answers the address, and `child_grid_id` and
`link_target_id` hold the address verbatim.

That is because a client stands on these ids. A pane holds its anchor grid
id and its content tile id, and the URL projects both; renaming either
under the user's hand — which minting did, the moment a descent's own
reframe wrote — left the pane naming something the listing does not
contain, and the grid went blank with nothing said (#297). The address
cannot do that: it names the thing by what it is.

Row ids stay resolvable as addresses forever, in both directions
(`Adapter.resolveTile`, `Adapter.resolveGrid`), so a link stored under
the older rule keeps naming the thing it named; re-storing one canonicalizes
it forward (`namespace.Minter`). What a retirement destroys is the row: a
recreated key comes back at the same address with a fresh row, holding none
of the arrangement the retired one held, and every link stored against
that row stays dead.

A connection name in `retired_names:` never returns; a name the config merely
stopped declaring is not retired, and resolves again the moment it is
declared again. Home is only ever letter + digits — it is not a plugin.
