# gridwell-desktop

Electron shell for Gridwell. It hosts the Go backend as a loopback sidecar and
renders live URL tiles as native `WebContentsView` overlays floated over the
WASM canvas.

## Dev

Build the sidecar + wasm and install the desktop deps once (online):

```sh
cd ../.. && make vendor
```

Then create a home and run the app (offline from here on):

```sh
cd ../.. && make init            # once: register a localdb plugin in ~/.gridwell
make launch                      # against ~/.gridwell
GRIDWELL_HOME=/path make launch  # against another home
```

`make launch` builds the sidecar + wasm, compiles the TS, and runs Electron
with Chromium's OS sandbox on (no `--no-sandbox`): live URL tiles load
untrusted web content, so the sandbox is the containment that matters, and a
modern Linux/WSL2 kernel with unprivileged user namespaces needs no setuid
helper for it. The app spawns `<repo>/gridwell serve` with an ephemeral
loopback port as `--bind-default`, waits for the server to announce its actual
bound address, and loads the renderer from that origin. An explicit `bind:` in
`~/.gridwell/server.yaml` overrides the ephemeral default — that is how one
server instance serves both the desktop window and a phone browser (see the
repo README's "Browser client / phone access"). `serve`
requires `~/.gridwell/server.yaml`; each plugin's SQLite DB lives at
`~/.gridwell/db/<id>/store.db`, derived from its id — there is no fallback DB.

Overrides (env): `GRIDWELL_SIDECAR` (binary path), `GRIDWELL_STATIC`
(web assets dir), `GRIDWELL_HOME` (config + DB root, default `~/.gridwell`).

## Test

```sh
npm run typecheck         # tsc --noEmit
npm test                  # pure-logic unit tests (lines, viewutil)
npm run test:integration  # xvfb: registry hosts a real WebContentsView + capture
npm run test:bridge       # xvfb: full preload->ipcMain->registry->freeze path
```

Launching the GUI needs a display (on a display-less box, `xvfb-run -a
electron .`). If a kernel ever lacks unprivileged user namespaces and Electron
refuses to start, `--no-sandbox` is the escape hatch — but it disables the
containment for untrusted URL-tile content, so prefer enabling user namespaces.

## How live URL tiles work

A frozen URL tile is a JPEG drawn into the canvas. When you descend + refresh,
the renderer calls `window.gridwell.placeWebview` and the main process floats a
native `WebContentsView` over the pane's content box, on the ONE shared
persistent session partition (`persist:gridwell` — the Chromium session is
host-local, 2026-07-26; your logins everywhere, mounts included).
`syncURLViews` tracks the view to its content box every frame and parks
it off-screen during drag/palette gestures so canvas overlays paint on top. A
`MirrorPump` captures live views on a modest cadence and pushes frames to the
renderer so other panes showing the same tile mirror navigation. On ascend the
view is captured one last time, the frame + URL + title are persisted via the
`SetURLState` RPC, and the view is destroyed.

## Packaging (hermetic AppImage)

The build is split into one online bootstrap and any number of offline builds.

```sh
cd ../.. && make vendor   # ONE online step: pins + caches everything
cd ../.. && make dist     # offline: produces out/Gridwell-<ver>.AppImage
```

`make vendor` runs `npm ci` against the committed `package-lock.json` and
warms three repo-local caches under `apps/desktop/.cache/` (gitignored):

- `npm/` — the npm package store (`npm ci --offline` source).
- `electron/` — the Electron runtime zip (`electron_config_cache`).
- `electron-builder/` — electron-builder's helper binaries and the AppImage
  runtime (`ELECTRON_BUILDER_CACHE`).

After that, `make dist` needs no network. It produces a single self-contained
`Gridwell-<ver>.AppImage` bundling the Electron runtime, the **static** Go
sidecar (built `CGO_ENABLED=0`), and the wasm assets. The AppImage uses
electron-builder's static runtime, so it does **not** depend on FUSE and runs
on any glibc desktop:

```sh
./out/Gridwell-*.AppImage                       # run it
./out/Gridwell-*.AppImage --appimage-extract    # inspect the bundle
```

`extraResources` bundles the sidecar binary (`../../gridwell`) and the web
assets (`../../web`) under the app's `resources/` dir; `paths.ts` resolves them
from `process.resourcesPath` at runtime, falling back to the dev tree.

**Other platforms:** the linux target is AppImage; mac is dmg and win is nsis
(see `build` in `package.json`). The bundled sidecar is host-arch — to package
for another OS/arch, cross-compile it first and build on/for that platform:

```sh
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o ../../gridwell ./cmd/gridwell
```
