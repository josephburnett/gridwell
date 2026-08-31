# The anatomy of an id

An id is a chain of segments joined by `/`, read left to right. Each hop
peels one segment; the segment's shape says what it is:

| Shape | Example | Means |
|---|---|---|
| letter-leading | `ngkwanw`, `fa21d5d1…` | a namespace: node, plugin, or connection (7-char base36 or legacy 32-hex) |
| digits | `14` | a minted row (tile or grid) in the owner's store — permanent, never reused |
| `~` + base64url(key) | `~L2hvbWUvam9l` | a plugin entry named by its own key — no row; mints to digits on first durable touch |

## Examples

```
52f8374f…/14                    home tile 14 (node id = home's id)
ngkwanw/7                       minted tile 7 in gitlab's namespace
fa21d5d1…/~L2hvbWUvam9l         untouched fs entry /home/joe
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
- `Reference` (dashed = link) is derived from a chain crossing
  namespaces — never stored.

## Stability

Digits are never reused. A `~` id is as stable as the plugin's key (keys
are forever, per the plugin contract). Retired connection names never
return. Home is only ever letter + digits — it is not a plugin.
