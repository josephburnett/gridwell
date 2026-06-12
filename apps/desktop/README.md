# gridwell-desktop

Electron shell for Gridwell. Hosts the Go backend as a `--no-browser`
sidecar and (from Phase 3) renders live URL tiles as native
`WebContentsView` overlays instead of rod-driven Chromium JPEG streams.

See `../../docs/electron-migration.md` for the full plan.

## Dev

Build the sidecar + wasm from the repo root first:

```sh
cd ../.. && make build
```

Then, from this directory:

```sh
npm install
npm start          # tsc + electron .
```

The app spawns `<repo>/gridwell serve --no-browser`, waits for it to
listen on an ephemeral loopback port, and loads the renderer from there.
The SQLite DB lives in Electron's `userData` dir.

Overrides (env): `GRIDWELL_SIDECAR` (binary path), `GRIDWELL_STATIC`
(web assets dir), `GRIDWELL_DB` (db path).

## Test

```sh
npm run typecheck     # tsc --noEmit
npm test              # pure-logic unit tests (lines, viewutil)
npm run test:integration  # xvfb: registry hosts a real WebContentsView + capture
npm run test:bridge       # xvfb: full preload->ipcMain->registry->freeze path
```

Launching the GUI needs a display (on a headless box, `xvfb-run -a
electron . --no-sandbox`).

## How live URL tiles work

A frozen URL tile is a JPEG drawn into the canvas (unchanged from the old
design). When you descend + refresh, the renderer calls
`window.gridwell.placeWebview` and the main process floats a native
`WebContentsView` over the pane's content box, on a persistent per-tile
session partition (`persist:tile-<objectId>` — real cookies, isolated per
tile). `syncURLViews` tracks the view to its content box every frame and
parks it off-screen during drag/palette gestures so canvas overlays paint on
top. A `MirrorPump` captures live views on a modest cadence and pushes frames
to the renderer so other panes showing the same tile mirror navigation. On
ascend the view is captured one last time, the frame + URL + title are
persisted via the `SetURLState` RPC, and the view is destroyed.

## Packaging

```sh
npm run dist:dir   # unpacked app in out/<platform>-unpacked/
npm run dist       # platform installer (see "build" in package.json)
```

`extraResources` bundles the Go sidecar binary (`../../gridwell`) and the web
assets (`../../web`) under the app's `resources/` dir; `paths.ts` resolves
them from `process.resourcesPath` at runtime, falling back to the dev tree.

**Cross-platform:** the bundled sidecar is host-arch. To build for another
OS/arch, cross-compile the Go sidecar first and point the build at it:

```sh
GOOS=darwin GOARCH=arm64 go build -o ../../gridwell ./cmd/gridwell   # from repo root
# then: npm run dist   (on/for the target platform)
```
