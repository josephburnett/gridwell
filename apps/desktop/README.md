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
npm run typecheck                              # tsc --noEmit
node --test --import tsx src/main/lines.test.ts # pure-logic unit tests
```

Launching the GUI needs a display (on a headless box, `xvfb-run -a
electron . --no-sandbox`).
