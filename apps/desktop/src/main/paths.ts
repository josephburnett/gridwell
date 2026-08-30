import { app } from 'electron';
import * as path from 'node:path';
import * as fs from 'node:fs';

// Resolving the Go sidecar binary and the static (wasm) dir override.
//
// Dev layout (running `electron .` from apps/desktop):
//   <repo>/apps/desktop/dist/main/index.js   ← app path is apps/desktop
//   <repo>/gridwell                          ← sidecar binary
//   <repo>/web                               ← static assets
//
// Packaged layout: the sidecar and web are bundled as resources under
// process.resourcesPath. Env overrides win, then the packaged resources,
// then the dev tree.

function repoRoot(): string {
  // apps/desktop → ../../ is the repo root in the dev tree.
  return path.resolve(app.getAppPath(), '..', '..');
}

export function sidecarBinary(): string {
  const env = process.env.GRIDWELL_SIDECAR;
  if (env && fs.existsSync(env)) return env;

  const packaged = path.join(process.resourcesPath ?? '', 'gridwell');
  if (fs.existsSync(packaged)) return packaged;

  const dev = path.join(repoRoot(), 'gridwell');
  return dev;
}

// staticDir is the override only; null means none. The gridwell binary
// embeds the web client (web/embed.go), so the server serves it with no
// --static in either layout. GRIDWELL_STATIC is for the e2e harness and for
// iterating on web/ without rebuilding the binary.
export function staticDir(): string | null {
  const env = process.env.GRIDWELL_STATIC;
  if (env && fs.existsSync(env)) return env;
  return null;
}
