# Gridwell → Electron migration

Status: in progress (branch `electron-pivot`).

## Why

URL tiles render via a headless Chromium driven by `rod`. That browser is
fingerprinted as automation (Cloudflare, login walls) no matter how we patch the
user-agent or `navigator.webdriver`. The fix is to stop driving a browser and
instead host a **real** browser engine inside a desktop shell: an Electron
`WebContentsView` per live URL tile, rendering the page natively with a
persistent real profile and no CDP automation driver.

## The one idea

The whole Gridwell UI is a single `<canvas>` (Go→WASM). Every tile is drawn as
pixels. URL tiles are the exception that forces the rod hack: a canvas can't host
a live web page, so today the page is rendered elsewhere and streamed in as JPEG.

After the migration:

| Tile state | Today | After |
|---|---|---|
| Frozen URL (preview) | JPEG drawn into canvas | JPEG drawn into canvas — **unchanged** |
| Live URL (descended) | rod → JPEG over WS → canvas | native `WebContentsView` floated over the pane rect |
| Mirror to other panes | rod JPEG → preview blob | `capturePage()` → preview blob — same effect, new source |

Only URL tiles change. Text, wells, file/process wells, blackhole, and shell all
stay exactly as they are.

## Process & IPC topology

```
Electron main (TS, thin)              Go sidecar (existing backend, rod deleted)
  · BaseWindow + root WebContentsView    · Connect-RPC + SQLite + COW
  · live URL WebContentsViews            · shell PTY (WS), SSE Subscribe
    (create/move/remove, per partition)  · serves wasm assets on loopback
  · capturePage → frames                 ▲
  · spawns + supervises sidecar          │ loopback HTTP/WS/SSE (UNCHANGED)
        ▲   │ Electron IPC (NEW,         │
        │   │ URL tiles only)            │
        │   ▼                            │
        └ Renderer (Go→WASM canvas) ─────┘
            · gesture/render engine UNCHANGED
            · live URL path now talks to main over the preload bridge
            · persists freeze frame/url via a new RPC
```

Two IPC channels:
1. **Renderer ↔ sidecar** — loopback HTTP/WS/SSE. Unchanged. The store stays the
   single source of truth; the renderer is the only RPC client.
2. **Renderer ↔ main** — Electron `contextBridge`. New. Only for URL webview
   placement/capture. Main is near-stateless: it owns native views, nothing else.

## Freeze persistence

Today the *server* writes the preview blob when the rod session closes
(`server/url_stream.go:closeSession` → `store.SetURLPreview` / `SetURLString` /
`SetTileAlt`). Those store methods already exist. In the new world main has the
final capture bitmap, so we expose them via one RPC, `SetURLState(tileID, jpeg,
url, title)`, which the renderer calls on ascend. Main never touches the store.

## Process-count safety

Only *visible live* panes hold a `WebContentsView` (a handful). Every frozen tile
is a canvas JPEG with zero process cost. Gridwell's existing "one live session per
tile" rule is exactly what keeps Chromium process count bounded.

---

## Phases

Each phase builds, has tests at the levels noted, and is committed on its own
(per the project's commit-incrementally rule). Test levels:

- **L1 unit** — pure logic: Go `go test`, TS `node:test`/vitest.
- **L2 build** — `go build ./...`, `GOOS=js` wasm build, `tsc --noEmit`, electron bundle.
- **L3 integration** — sidecar spawn + RPC roundtrip; Electron main boot with a
  stub renderer; capture pipeline against a known test page (headless via xvfb
  where possible).
- **L4 e2e (manual, needs a display)** — launch, descend, go live, navigate,
  ascend→freeze, mirror to a second pane, gesture hides the view. User-driven.

### Phase 1 — Electron shell hosting the app unchanged
`apps/desktop/` with main that spawns `gridwell serve` (rod still present) and
loads `http://127.0.0.1:<port>/` in one root `WebContentsView`. URL tiles still
use the old path. Proves sidecar supervision, window, root view, RPC/WS/SSE
through Electron.
- L1: sidecar port-handshake parser; main config.
- L2: `tsc --noEmit`, electron packages, `go build`.
- L3: spawn sidecar, wait for `listening on …`, hit `/` and an RPC; assert 200.
- L4: launch, see the grid, descend into a well/text tile.

### Phase 2 — IPC contract + webview manager (no renderer wiring)
Main modules: `ipc.ts` (typed messages), `webviews.ts` (registry: place/move/
remove a `WebContentsView` on `session.fromPartition('persist:tile-<objId>')`),
`capture.ts` (throttled `capturePage` → frame), `preload.ts` exposing
`window.gridwell.{placeWebview,setBounds,removeWebview,setHidden,onFrame,onNav}`.
- L1: registry add/move/remove and rect math; partition-name derivation; IPC
  message validation.
- L2: `tsc --noEmit`.
- L3: standalone harness opens example.com offscreen, asserts a non-empty frame
  and a nav event.

### Phase 3 — Renderer: swap the URL live path to webviews
Replace `urlStreamConn` (WS+JPEG) with `webviewClient` over the preload bridge,
reusing the existing lifecycle seams:
- `openURLStream` → `placeWebview(tileID, url, partition, contentRect)`
- per-draw, each live URL pane pushes its content rect → `setBounds`
- `closeURLStream` → `removeWebview` (+ final freeze via `SetURLState`)
- delete URL input-forwarding (native view handles its own input)
- capture frames → `urlPreview.PutWildcard` (mirror)
- gesture/modal active → `setHidden(true)` so overlays can paint
Add the `SetURLState` RPC (proto + handler + store wiring; store methods already
exist).
- L1: contentRect→bounds (DPR) mapping; "should this pane be live?" predicate;
  hide-during-gesture predicate; RPC handler in Go (`go test`).
- L2: `GOOS=js` wasm build; `go build ./...`; `tsc`.
- L3: RPC roundtrip writes preview+url+title to the store.
- L4: descend a URL tile → native page; navigate; ascend → frozen frame persists;
  open same tile in a 2nd pane → mirror updates; start a drag → view hides.

### Phase 4 — Delete rod and the JPEG-stream server path
Remove `internal/urldriver/`, `internal/server/url_stream.go` + the
`/rpc/URLStream` route, `client/urlstream/`, `client/wasm/url_stream_client.go`,
the `go-rod` dependency, the `chromium/` profile dir, and the CLI
`--browser`/`open_browser` wiring. `serve` becomes a pure loopback sidecar.
- L1/L2: `go build ./...`, `go test ./...`, `GOOS=js` wasm build all green.
- L3: sidecar boots with no browser flags; shell + file + process tiles unaffected.
- L4: full smoke of all seven tile kinds.

### Phase 5 — Packaging + polish
`electron-forge` (or builder) targets for Linux/macOS/Windows, bundling the
cross-compiled sidecar binary + wasm assets as resources; SQLite in `userData`.
URL back button → `webContents.goBack()`. Capture cadence tuned (only mirror when
a tile is shown in ≥2 panes; always capture once on ascend).
- L2: produce a Linux artifact; `tsc`.
- L3: packaged app boots, finds its bundled sidecar, opens the DB in userData.
- L4: install/run the packaged build.

## Risks
- **DPR/bounds alignment** — `WebContentsView.setBounds` is DIP; canvas math is CSS
  px. Must line the view up exactly with its outline. Validate early (Phase 3 L4).
- **Z-order** — native views always sit above the canvas DOM; modals/palette/drag
  ghosts must hide live views (the `setHidden` path). Designed in from Phase 2.
- **Capture throughput** — published Electron OSR fps numbers were refuted in
  research; benchmark, don't trust. Mirror cadence is tunable and only matters
  for multi-pane tiles.
- **Cloudflare** — the user has accepted this architecture regardless of the
  anti-bot outcome, so it is no longer a go/no-go.
