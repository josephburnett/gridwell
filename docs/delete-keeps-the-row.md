# Delete retires only what is gone

## The bug

Trash a gitlab todo tile the user has moved or resized and it snaps
back to its hinted placement; any link dragged to it (a weekly plan
naming a specific todo) goes dead. Both are one bug, on the node side.

The gitlab plugin redefined `Delete` as "mark the todo done at GitLab;
the tile stays and changes state" (gridwell-plugins eacce96). But
`Adapter.DeleteTile` (`internal/pluginhost/adapter.go`, ~line 954)
still assumes delete means gone: after the plugin's `Delete` succeeds
it unconditionally `mem.Retire(ref.id)`s the minted row.

The row is where both durable facts live:

- **Placement.** The next listing still names the todo (a done todo
  never leaves the gitlab plugin's listing), but the entry is now
  rowless, so `Overlay` derives its placement from the plugin's
  calendar hint — the snap-back. The next touch mints a *fresh* row
  with a new id.
- **References.** `MintRef` (`adapter.go`, ~line 616) canonicalizes
  every stored reference to a row id — "a reference at rest must name
  a row" — so a link tile's `link_target_id` is `<uuid>/<row-id>`.
  `resolveTile` answers NotFound for a tombstoned row (~line 517), so
  the link's Probe reads GONE and its content reads dead.

The plugin's own identity is already stable: its key is
`todo:<gitlab-id>`, forever, and its `Probe` never answers GONE for a
remembered todo. The identity that breaks is the node's row.

## The fix

`DeleteTile` stops retiring unconditionally. After the plugin's
`Delete` succeeds, ask the plugin whether the key is still there —
`cp.Probe(ref.key)` — and retire the row only on a definitive
`PRESENCE_GONE`.

This reuses the exact arbitration the non-authoritative listing path
already runs (`synthesize`'s probe arm, `adapter.go` ~line 450): only
the source's word, or a GONE probe, removes a minted row. DeleteTile
becomes consistent with that rule instead of the one exception to it.

Behavior by plugin:

- **gitlab**: Probe answers PRESENT for a remembered todo → the row
  survives. Placement kept (no snap-back), row id kept (links keep
  resolving), and the next listing's `Refresh` repaints the label with
  the ✅ done mark on the same row.
- **fs**: Delete removes the file; Probe answers GONE → the row
  retires exactly as today. No behavior change.
- **Probe transport failure or UNSPECIFIED**: keep the row. Same rule
  as the listing path — a failed read must never read as GONE. An
  authoritative plugin's next listing sweeps the row anyway if the
  thing is really gone, so keeping it on doubt costs nothing durable.

Alternative considered and rejected: a `kept` bit on
`pluginv1.DeleteResponse`. Semantically cleanest (the plugin knows
what its Delete means) but it is a proto change across two repos, and
the probe already answers the same question over the existing wire.

## Where

All in `internal/pluginhost/adapter.go`, `DeleteTile`:

1. Keep the existing early paths: unresolvable id → idempotent
   success; plugin `Delete` error → surface it; `ref.id == 0`
   (untouched entry, no row) → nothing to retire.
2. Where it now calls `a.mem.Retire(ref.id)`, first
   `a.cp.Probe(ctx, &pluginv1.ProbeRequest{Key: ref.key})`. Retire
   only when the probe answered cleanly with `PRESENCE_GONE`.
3. `emitGridChanged` stays in all cases — the delete changed the
   source either way, and the client must refetch (for gitlab, that
   refetch is what paints the done mark).

## Tests

- Adapter test: minted row, plugin whose Delete succeeds and whose
  Probe answers PRESENT → row still live after DeleteTile, same id,
  same placement; listing after shows the entry on that row.
- Adapter test: Probe answers GONE → row tombstoned (today's fs
  behavior, pinned).
- Adapter test: Probe fails with a transport error → row kept.
- The gitlab seam tests (`internal/server/gitlab_seam_test.go`) are
  the cross-repo gate; extend them with the user journey itself:
  place/move a todo tile, link to it from another grid, trash it, and
  assert the tile's id and placement held and the link still resolves.
  Note the ledger lesson in that file's history (2c306c8d): the seam
  tests only gate when they are actually run against the peer repo's
  current plugin.

## Contract line

A minted row retires only when its source says the key is gone — by
authoritative absence (Sweep), by a GONE probe (non-authoritative
listings, and now DeleteTile). A plugin whose Delete transforms the
thing rather than removing it keeps its rows, so placement and stored
references survive the gesture.
