# Rootless Launcher Rework — Plan

Status: COMPLETE. This doc is the durable record; the live todo list is in the
task tool. The rootless launcher is fully implemented, tested, and green
(33 packages + wasm build).

DONE:
- Stage 16: ListPlugins RPC + federated Subscribe (e5e8c78)
- SV1: tmux/shell keyed by qualified <uuid>/<tile-id> (41558d7)
- SV2: preview endpoint routes qualified id to owning plugin (7bfadf8)
- SV3: Mount RPC + fs Attach defaults to configured root (98055d1)
- SV4/5/6: drop the root — Bootstrap empty, SetRootView no-op, GetBlob
  Unimplemented (use GetTileContent), route() requires qualified ids, serve.go
  synth default localdb (not root), removed cfg.Root/primaryUUID/rootClient (cc5b995)
- PluginInfo.root_grid_id: ListPlugins reports each plugin's qualified root grid
  for click-enter (2a6e723)
- CL1/CL2: per-pane nav stack — pane.Anchor (plugin-root grid; ""=launcher),
  Path (well descents within the anchor), Up []Frame (portal ascent stack).
  gridIDForPane/gridIDForPathFrom resolve the leaf grid from anchor+path. URL
  encodes the anchor in the `a=` param; applyURLOnBoot restores it. Root-view
  persistence is a no-op (viewport lives in the URL). (f750f1f)
- CL3/CL4: data-driven + menu — paletteItems(pane) = configured plugins (config
  order) + tile primitives (only when the current grid is writable). Plugin
  click = enterPlugin (push frame, re-anchor at plugin root); plugin drag =
  mountPluginAtCell (Mount RPC, writable grids only); primitive drag = create.
  Portal ascent (canAscend/ascendPane Up-stack case) pops back to the launcher
  or prior plugin. (f44abf9)
- CreateFileWell/CreateProcessWell retired end to end (proto+gen+rpc+server+
  tests); Mount is the only path; dead createExitWell/FirstByKind removed.
  (4d3a404)
- Centered launcher: the + button sits at the pane center when the anchor is
  empty (palette.Layout.Centered), returning to lower-right inside a plugin.
  (aabd92c)
- Tests: pane portal-stack round-trip + Clone deep-copy, URL anchor round-trip,
  palette centered layout, server Mount (fs+proc) + fs lifecycle e2e. (f553ac5)

Not unit-tested (thin glue over tested pure cores + RPCs, per CLAUDE.md's
glue-code exception): the wasm enterPlugin/ascendPortal/mountPluginAtCell wiring
and the full browser interaction flow (empty start → enter → primitives →
drag-mount → click-enter + ascend → split start panes). The pure pane stack,
URL anchor, palette layout, and server Mount/descend/move/delete are all
covered.

## Goal

Remove the privileged "root" entirely. Startup = empty panes, each with a
centered "+" launcher. The "+" menu lists configured plugins (config order) +
primitives (only when the current grid accepts new tiles). Plugins are entered
(click → descend, stacking) or mounted (drag → exit-well link). tmux/shell and
the preview endpoint are keyed by fully-qualified `<plugin-uuid>/<tile-id>` so
multiple localdb plugins coexist without id collisions.

## The "+" menu (palette) — interaction spec

Always present: lower-right of the active pane; **centered** when the pane is at
the empty start. Contents:
- **Primitives** (well, text/markdown, url, shell): shown only when the CURRENT
  grid's plugin is writable (localdb). Hidden in read-only (fs/proc) grids and
  at the empty start.
- **Plugins**: every configured plugin, in config order. Always shown.

Gestures:
- **left-click (no drag) a plugin** → descend into it (push a portal frame;
  ascend returns). Works everywhere incl. read-only and empty start.
- **drag a primitive** onto the grid → create that tile (writable grids only).
- **drag a plugin** onto the grid → mount it as an exit-well link (writable only).
- **right-click anywhere on the menu** → ascend (pop the pane's nav stack).

## Pane navigation model (the novel/hard part)

Replace "path = list of well tile ids walked from one root" with an explicit
per-pane NAV STACK of frames:

    frame = { gridID string, viaWell string }   // viaWell="" → portal/enter frame

- Empty stack → start screen (blank + centered +).
- Enter plugin X (click): push `{gridID: <X-root qualified>, viaWell: ""}`.
- Descend well W: push `{gridID: W.ChildGridID, viaWell: W.ID}`.
- Ascend: pop one frame.
- current grid = top frame's gridID (none = start).
- RPC Path (well_ids for COW) for an op in the current grid = the `viaWell` ids
  from frames ABOVE the last portal frame (the trailing run within the current
  plugin) — exactly what the server's `localPathFor` already expects.

URL encodes the nav stack per pane (portal vs well frames); restored on reload.
This supersedes `pane.Pane.Path []string` + `gridIDForPath`; migrate urlsync,
gridpath, descent/ascent, drop_target, render carefully.

## Server work

- **SV1 — tmux/shell qualified keys.** `shellStreamer` + `internal/tmux` key
  sessions by qualified `<uuid>/<id>` (sanitize `/` and `.` for tmux session
  names, e.g. `gridwell-<uuid>_<id>`). The shell WS handler reads the qualified
  tile_id, routes GetTile to the owning plugin, and keys the session by the
  qualified id. DeleteTile-reap + orphan-sweep use qualified ids (Probe the
  owning plugin). `activeShellSessions` keyed by string. Removes the
  "single primary localdb for shell" assumption.
- **SV2 — preview endpoint qualified.** `/preview/tile/<uuid>/<id>` → route
  GetTile/GetTilePreview to the owning plugin (embed hrefs already qualified).
- **SV3 — Mount RPC.** `Mount(plugin_uuid, grid_id, path, x, y, w, h) →
  TileResponse`: server Attaches the plugin with default config (fs→its
  configured root, proc→pid 1, localdb→{}), qualifies the root, CreateWell{child}
  in the dest grid. Replaces CreateFileWell/CreateProcessWell (remove those +
  fs_path/pid request plumbing). fs plugin: store the configured `root` in
  Open/NewFactory and default Attach to it when no path is given.
- **SV4 — Bootstrap no-root.** Bootstrap returns no root grid (client starts
  empty). Drop server-side root-view persistence (rely on URL); SetRootView
  removed or no-op.
- **SV5 — serve.go default localdb.** When no plugins are configured, synthesize
  ONE localdb plugin (kind localdb, db_file = --db) — NOT a root. Remove
  `cfg.Root` + the `root:` config field + the primary concept.
- **SV6 — Server struct.** Remove `primaryUUID` / `rootClient`. id-less ops:
  Bootstrap (empty), Subscribe (federated). Everything else routes by qualified id.

## Client work

- **CL1 — App.** Drop `rootGridID`/`localdbUUID`-as-root. Fetch ListPlugins →
  `a.plugins`. Start with empty panes. The "local uuid" notion becomes per-grid
  (derive uuid from the grid id).
- **CL2 — Pane nav stack + empty start.** Implement the stack model above; render
  blank + centered + when a pane's stack is empty. Panes still split in the
  start state.
- **CL3 — Data-driven palette.** Entries = primitives (gated by current grid
  writability, looked up via grid uuid → `a.plugins[uuid].Writable`) + plugins
  (config order). Rework palette_draw (NumTiles, draw, hit-test), input
  (hit-test, drag-vs-click), drop-commit. Delete `templateKinds` hardcode +
  file-well/process-well entries and glyph cases.
- **CL4 — Gestures.** drag primitive = create; drag plugin = Mount RPC; click
  plugin = enter (push portal frame); right-click on menu = ascend.
- **CL5 — URL state.** Encode/restore the nav stack (portal + well frames) per
  pane. (isPluginTile / tileBody / exit-well rendering are already qualified.)

## Verify

`go test ./...` green; `GOOS=js GOARCH=wasm go build ./client/wasm` green. E2E:
empty start → enter a localdb → create primitives → mount a plugin (drag) →
enter a plugin (click) and ascend back → two split start panes → shells in two
different localdb plugins don't collide (qualified tmux keys).
