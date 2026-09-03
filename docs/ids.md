# The anatomy of an id

An id is a chain of segments joined by `/`, read left to right. Each hop
peels one segment; the segment's shape says what it is:

| Shape | Example | Means |
|---|---|---|
| letter-leading | `ngkwanw`, `fa21d5d1…` | a namespace: node, plugin, or connection (7-char base36 or legacy 32-hex) |
| digits | `14` | a minted row (tile or grid) in the owner's store — permanent, never reused |
| `~` + base64url(address) | `~L2hvbWUvam9l` | a plugin thing named by its own address — no row; mints to digits on first durable touch |

## Examples

```
52f8374f…/14                    home tile 14 (node id = home's id)
ngkwanw/7                       minted tile 7 in gitlab's namespace
fa21d5d1…/~L2hvbWUvam9l         the untouched fs grid of /home/joe
8aed…/eoifgyl/rp1nodeX/3        via connection eoifgyl → remote node → its tile 3
```

## Routing

`Server.resolve`, per hop: own-node id + digits → home; own-node id +
letter segment → that connection; any other letter segment → that plugin.
`~` and digits are always leaves-or-rows, never namespaces.

## Where each appears

- URL path: leading letter segments = the namespace chain, then
  doorway/tile segments (`~` counts as a tile segment, never a namespace).
- `child_grid_id` (a well or link's target grid) and `link_target_id` (a
  leaf link's target): full chains. A key names the entry; whether it
  stands for the entry's tile or its grid is positional, same as digits.
- `Reference` (dashed = link) is derived from the target arriving already
  qualified — never stored. A link inside one namespace is still a
  reference: a ctrl + right-drag makes one, and so does a same-namespace
  mount. A uuid comparison would miss both.

## What is inside a `~`

The payload is the owning namespace's own address for the thing, and no one
else opens it: to the URL grammar, the router, the /content/ door and the
client, a `~` segment is one opaque tile segment. `internal/pluginhost` is the
only reader, and it writes two forms (`address.go`):

| Position | Payload | Example |
|---|---|---|
| grid | the plugin's context key (its name for good) | `~` + b64(`/home/joe`) |
| tile | the context key, NUL, the entry key | `~` + b64(`/home` NUL `/home/joe`) |

A tile carries its context because a tile must be answerable on its own: the
node keeps no key→context index — that index is the row we are not minting —
and `plugin.v1` has no verb that describes one entry, so `GetTile` on an
untouched entry is one `List` of the context that names it.

## Stability

Digits are never reused. A `~` id is as stable as the plugin's key (keys
are forever, per the plugin contract).

A TILE reference at rest is never one: the router mints
(`namespace.Minter`) before a link or a clone stores a target, so
`link_target_id` holds digits. A `~` id already in a client's hands keeps
resolving after that mint — the answer just comes back named by the row.

A GRID keeps its `~` name for good, minted row or not, and a
`child_grid_id` into a plugin holds it verbatim. A grid is the one thing a
client is STANDING IN: a pane holds its anchor grid id, and renaming a grid
under a pane — which minting would do the moment the first tile in it was
dragged — would leave the pane naming a grid nothing answers to. Both forms
still resolve on the way in, so an older stored row id keeps working. Retired connection names never
return. Home is only ever letter + digits — it is not a plugin.
