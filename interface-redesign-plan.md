# Interface redesign: the reduced plugin surface (2026-07-26)

The governing document for the 2026-07-26 gRPC interface transformation.
Owner decisions live here; stages reference it; when this work is done the
durable parts move into `CLAUDE.md` / `ARCHITECTURE.md` and this file is
deleted (the `rootless-rework-plan.md` lifecycle).

## Motivation — what daily-driver use falsified

- **Session-per-plugin failed.** The owner wanted the ssh remote grid to use
  the *local* Chromium session; the per-plugin session blob and its
  hydrate/dehydrate choreography served nobody.
- **Network tunneling was never needed.** Live url tiles can always browse
  from the host's own network.
- **Browser live shells are unused** (phone use never touches them); the WS
  bridge exists only for that case.
- **File transfer is wanted** — content bytes must stream, not ride a capped
  unary field.
- **The writeback surface accreted**: six verbs, a vestigial `Path` on seven
  requests, two-writers-one-column on `alt`. The proto's own comments flag
  the asymmetries.

## Owner decisions (2026-07-26)

Each is an owner call made in conversation on 2026-07-26. Reversals are
marked; do not re-reverse without a new owner decision.

1. **Session is host-local.** ONE persistent local Chromium session serves
   every live url tile, regardless of owning plugin or remoteness.
   `GetSession`/`PutSession`/`BlobChunk`/`Info.has_session`, the `/session/`
   door, the Electron hydrate/dehydrate choreography, and
   partition-per-plugin all die. *Reverses* "the plugin is the session
   boundary" and its "copy the plugin's DB and you copy its logins"
   property. Chromium session state is machine-local by nature — a documented
   exception to charter §7 alongside the split-pane session tree.
2. **Network context is deleted.** `NetworkContext`/`ProxyEndpoint`,
   `Grid.proxy_endpoint`, the outermost-hop-wins transit stamping, and the
   SOCKS half of `sshdial` die. Live tiles browse from the host's network,
   always. *Reverses* the proxy-endpoint design.
3. **Browser live shells are dropped.** *Reverses* "shells stay live" from
   the browser-client work (the owner does not use shells from the phone).
   In browser mode shells render frozen previews like live url tiles
   (`client/caps`). The `/rpc/ShellStream` WS bridge and the wasm WS client
   die; shell transport becomes an Electron main-process gRPC `OpenShell`
   client bridged to the renderer's xterm over IPC.
4. **`Path` is deleted from the wire.** Every mutation is id-addressed +
   version-claimed. The move/clone cycle check ("cannot move a well into its
   own subtree") moves server-side: an ancestor walk from `dest grid_id` up
   through parent wells (each interior child grid hangs off exactly one well
   by construction). `gridSequence.wells` is dead (COW ghost) and goes with
   it.
5. **One way to write bytes: `WriteContent`.** A client-streamed,
   version-claimed, COMMIT-AT-CLOSE write — a broken stream leaves the old
   value intact; partial delivery is never visible. It replaces
   `UpdateText`, `SetPaneLayout`, and `CreateTile.data` (text/pane creation
   becomes create-then-write; a failure between leaves an empty tile —
   visible and deletable, not silent). `ReadContent` (server-streamed;
   chunk 1 carries `media_type` + `version`) replaces `GetTileContent`.
   Version semantics stay kind-determined in the one store table:
   text body → content edit (bumps); pane layout → framing-class (no bump).
6. **`SetTile` absorbs `SetTileAlt` and `SetContentZoom`** (both were
   path-free scalar writes by tile id). Precondition, per the proto's own
   comment: the shell title-capture path becomes VERSIONED (read row, claim
   version, retry once on conflict) — removing the two-writers-one-column
   overlap on `alt` instead of institutionalizing it.
7. **`PlaceTile` merges `MoveTile` + `ResizeTile`.** Placement is one fact:
   `(grid_id, x, y, w, h)`; one verb owns it. Cross-plugin placement is
   still refused (link/clone verdicts are client-side `DecideDrop`,
   unchanged).
8. **Link resolution moves server-side.** `ReadContent` and `GetTilePreview`
   on a leaf-link tile forward to `link_target_id` at the serving node (the
   router already routes qualified ids). The client's `rpc.Tile.ContentID`
   read-through becomes redundant for these verbs and is deleted with them;
   every caller (CLI, agent, future plugin) inherits the one resolution
   point.
9. **`SetRootView` survives** as the lone structural special case (plugin
   roots have no tile row).
10. **`GetTilePreview` stays unary; preview writes stay inside `SetTile`.**
    The url freeze is one atomic versioned write (url + title + jpeg +
    history); splitting it would un-atomize the freeze. If more byte
    channels are ever needed, `ReadContentRequest` grows a channel enum —
    additive.
11. **`OpenShell` stays PTY-shaped and shell-specific.** A future live tile
    kind (camera, tail -f) adds its own wire verb additively. No speculative
    `OpenChannel` generalization.

### Future-reserved shape (decided, NOT implemented — no consumer exists)

Recorded so the reduced surface never precludes them; implementing any of
these without a consuming plugin would be dead code.

- **Creation parameters as content.** Wells (and other kinds) may gain a
  content channel whose bytes are the tile's parameters (a search query, an
  ssh connection spec). Committed via the same `WriteContent`; the plugin
  materializes the child grid from them.
- **`Info.create_schemas: map<kind, json_schema>`**, stamped per-grid onto
  `GetGridResponse` by the serving node (the `writable` stamping seam) and
  passed verbatim through transit — so the schema of the grid you're
  dropping into survives any chain depth. Client renders a form from a
  deliberately small schema subset and pre-validates; the plugin validates
  authoritatively at commit.
- **No query editing.** A different search is a NEW grid in the search
  plugin, not an edit of an existing one. Grids stay as you made them.
- **ssh connections-as-data.** One ssh plugin hosting many connections,
  each a well whose params name `user`/`host`/`port` + a key *path*. The
  plugin mints a letter-leading short id per connection as a sub-namespace
  segment: `<ssh-plugin>/<conn>/<remote-plugin>/<id>` — namespaces all the
  way down; the URL grammar and transit chaining recurse unchanged.
- **Secrets are files on the plugin's own host.** Parameters reference
  secret material by path, resolved by the plugin where it runs (a plugin
  on the laptop reads a laptop file; a plugin on the server reads a server
  file — even when selected from the laptop through the interface). Secret
  bytes never ride tile content, never land in a plugin DB, never cross the
  wire. Schema `secret` hints mask *input*, not storage.

## Target surface (17 RPCs; was 22)

```proto
service Gridwell {
  // ── Lifecycle ──
  rpc Info(InfoRequest) returns (InfoResponse);        // − has_session, − network
  rpc Probe(ProbeRequest) returns (ProbeResponse);
  rpc ListPlugins(ListPluginsRequest) returns (ListPluginsResponse);

  // ── Reads ──
  rpc GetGrid(GetGridRequest) returns (GetGridResponse);   // Grid − proxy_endpoint
  rpc GetTile(GetTileRequest) returns (TileResponse);
  rpc GetTilePreview(GetTilePreviewRequest) returns (GetTilePreviewResponse); // link-resolving

  // ── Content bytes (values: finite, versioned, transactional) ──
  rpc ReadContent(ReadContentRequest) returns (stream ContentChunk);  // link-resolving
  rpc WriteContent(stream WriteContentRequest) returns (TileResponse);

  // ── Mutations (id + version claim; no Path anywhere) ──
  rpc CreateTile(CreateTileRequest) returns (TileResponse);   // metadata only; no data
  rpc SetTile(SetTileRequest) returns (TileResponse);         // + alt, + content_zoom
  rpc PlaceTile(PlaceTileRequest) returns (TileResponse);     // ⇐ MoveTile + ResizeTile
  rpc CloneTile(CloneTileRequest) returns (TileResponse);
  rpc DeleteTile(DeleteTileRequest) returns (DeleteTileResponse);
  rpc SetRootView(SetRootViewRequest) returns (SetRootViewResponse);

  // ── The one live wire ──
  rpc OpenShell(stream OpenShellRequest) returns (stream OpenShellResponse);
  rpc ShellSessionAlive(ShellSessionAliveRequest) returns (ShellSessionAliveResponse);

  rpc Subscribe(SubscribeRequest) returns (stream Event);
}

message ReadContentRequest  { string tile_id = 1; }
message ContentChunk        { bytes data = 1; string media_type = 2; int64 version = 3; }
message WriteContentRequest { string tile_id = 1; int64 version = 2; bytes data = 3; }
message PlaceTileRequest    { string tile_id = 1; int64 version = 2;
                              string grid_id = 3; int64 x = 4; int64 y = 5;
                              int64 w = 6; int64 h = 7; }
```

The interface's whole contract for any caller: **qualified id, version
claim, kind-dispatched semantics.** Exactly two byte-moving shapes: content
streams (values — finite, repeatable reads; transactional writes) and the
shell wire (bytes in motion — unbounded, duplex, every byte a side effect).
Nothing in the interface knows what a pane, a descent path, or a session is.

## Deletion inventory

RPCs: `GetSession`, `PutSession`, `GetTileContent`, `UpdateText`,
`SetTileAlt`, `SetPaneLayout`, `SetContentZoom`, `MoveTile`, `ResizeTile`.
Messages/fields: `Path` (+ 7 request fields), `BlobChunk`,
`NetworkContext`, `ProxyEndpoint`, `Info.has_session`, `Info.network`,
`Grid.proxy_endpoint`, `CreateTileRequest.data`, all `*Path` fields.
Server: `session.go` + `/session/` door, `shell_stream.go` + `/rpc/
ShellStream`, `localPathFor`, per-verb path plumbing, client-side-only alt
writer.
Store: `buildGridSequence`/`checkPathLeaf`/`gridSequence` (replaced by the
ancestor walk + direct grid checks), `UpdateText`/`SetPaneLayout`/
`SetContentZoom`/`SetTileAlt`/`MoveTile`/`ResizeTile` verbs (logic folds
into the new verbs).
Client: `shell_stream_client.go` WS transport (xterm stays; bytes arrive by
IPC), path threading (~20 call sites), `rpc.Tile.ContentID` read-through
for content/preview, `UpdateText`/`SetPaneLayout`/`SetContentZoom`/
`SetTileAlt` client methods.
Electron: session hydrate/dehydrate in `session.ts` (partition bookkeeping
simplifies to the one local session), per-plugin partition wiring in
`webviews.ts`.
sshdial: the SOCKS listener (`socks.go`) and proxy plumbing.

## Migration strategy: expand → migrate → contract

Every commit lands green on `make check`; stages touching the native layer
carry `make check-electron` / `make check-e2e`; federation-visible stages
carry `make check-federation`. Old and new verbs coexist during migration —
the proto grows first, call sites move verb-by-verb, and the old surface is
deleted only in the final contraction, so the app works end-to-end at every
commit.

- **Stage 1 — proto expand.** Add `ReadContent`/`WriteContent`/`PlaceTile`
  (+ messages) alongside the old verbs; buf generate; drift-lint stays
  green (no column changes).
- **Stage 2 — store + localdb.** Implement the new verbs in `store` +
  `localdb` dispatch: streamed content read/write with commit-at-close;
  `PlaceTile`; the server-side ancestor-walk cycle check; versioned title
  capture; `SetTile` alt/content_zoom arms. Unit tests: commit-at-close
  abort leaves old bytes; cycle refusal without any client path; placement
  merge covers move-, resize-, and both-at-once shapes; version table
  rows (text bumps, pane layout doesn't).
- **Stage 3 — plugins.** `fs`/`proc`/`nodegrid` implement `ReadContent`
  (+ `PlaceTile` where they accept placement); `proxy` forwards the new
  streams; dispatch/seam tests extend.
- **Stage 4 — server router.** Route the new verbs; implement server-side
  link resolution in `ReadContent`/`GetTilePreview` (+ seam test through a
  two-plugin link); `nodeexport` gains the `WriteContent` client-stream
  route.
- **Stage 5 — client migrate.** wasm call sites move verb-by-verb: text
  save via `WriteContent` (the `{bytes, base, dirty}`/`SaveBasis` cache
  contract is PRESERVED EXACTLY — `ReadContent` chunk-1 version is the
  fetch basis, `WriteContent` response version the new basis; all flushes
  still through `text_flush.go`); pane layout persister; alt + content
  zoom via `SetTile`; move/resize gestures via `PlaceTile`; create-then-
  write for text/pane creation; path threading deleted.
- **Stage 6 — shell transport.** Electron main-process gRPC `OpenShell`
  client (h2c to the sidecar) bridged over IPC to the renderer's xterm;
  decision logic extracted to a testable module; delete the WS bridge and
  wasm WS client; `client/caps` gates shells to frozen previews in browser
  mode. check-electron + a new e2e spec (shell round trip over the new
  transport) + check-web spec update (shell tile renders frozen, no live
  affordance).
- **Stage 7 — session simplification.** One persistent local session; strip
  hydrate/dehydrate, `/session/` door, per-plugin partitions. e2e: a live
  url tile in a second plugin shares the local session (cookie visible
  across plugin boundaries); reload keeps it.
- **Stage 8 — network deletion.** `NetworkContext`/`proxy_endpoint`/SOCKS
  out; federation gate proves a remote mount's grids read/write/shell
  through the chain without a proxy endpoint.
- **Stage 9 — contract + sweep.** Delete the old RPCs and every item in the
  deletion inventory; regenerate; dead-code sweep; `CLAUDE.md` +
  `ARCHITECTURE.md` updated (interface section, session boundary, charter
  exception list, seam catalog); this file deleted; memory updated.

## Test plan (the seams that must be crossed)

- **Store:** commit-at-close abort; cycle walk; placement; version table.
- **Plugin dispatch:** each plugin's new-verb behavior (fs `ReadContent` of
  a real file incl. >1-chunk sizes; proc `@info`).
- **Server seam:** link read-through across two real plugins; transit
  qualification of `ContentChunk`-carrying verbs unchanged.
- **Federation (`make check-federation`):** content stream + shell through
  a real ssh chain; the event step keeps proving foreign-writer reconcile.
- **e2e (`make check-e2e`):** text edit/save/reload round trip on the new
  write path; drag-move and resize gestures on `PlaceTile`; leaf-link
  content read-through; shell create/attach/detach/delete over the IPC
  transport; shared-session cookie crossing plugins.
- **web (`make check-web`):** shells frozen in browser mode; touch + URL
  behaviors unaffected.
- Known confound: issue #195 (content-zoom:89, stack-hygiene) fails under
  full-suite load — rerun isolated before attributing.

## Risks

1. **The text save path was stabilized eight days ago** (SaveBasis,
   cross-tile stomp). Stage 5 re-plumbs its transport but must not change
   the cache contract; the existing `client/cache` unit tests and
   `foreign-writer.spec.ts` are the regression net and must stay green
   untouched.
2. **Stage 6 is native-layer streaming** — the charter's bug home. Bridge
   lifecycle (attach, detach, teardown on delete, resize) needs its
   decision logic in a pure module with unit tests, plus the e2e spec.
3. **Streaming through go-plugin + transit chains** — `proxy.go` already
   forwards bidi streams (OpenShell), so the shapes are proven; the new
   client-stream (`WriteContent`) is the one novel routing case
   (`nodeexport` routes it by first-message id, like `PutSession` did).
