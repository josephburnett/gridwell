# Rootless Launcher Rework — Plan

Status: in progress. This doc is the durable plan; the live todo list is in the
task tool. Update both as work lands.

DONE so far:
- Stage 16: ListPlugins RPC + federated Subscribe (e5e8c78)
- SV1: tmux/shell keyed by qualified <uuid>/<tile-id> (41558d7)
- SV2: preview endpoint routes qualified id to owning plugin (7bfadf8)
- SV3: Mount RPC + fs Attach defaults to configured root (98055d1)

REMAINING:
- SV4/5/6: Bootstrap no-root; serve.go synth default localdb (not root); remove
  cfg.Root + root: field + Server.primaryUUID/rootClient. (Currently primaryUUID
  is unused by shell/preview after SV1/SV2 — only Bootstrap/SetRootView/rootClient
  reference it.) Also retire CreateFileWell/CreateProcessWell once client uses Mount.
- CL1/CL2: pane nav-stack + empty start state (the hard part).
- CL3/CL4: data-driven palette + gestures (uses ListPlugins + Mount).
- CL5: URL nav-stack encode/restore + e2e.

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
