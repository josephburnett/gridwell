import { app } from 'electron';
import * as path from 'node:path';
import * as fs from 'node:fs';

// Resolving the Go sidecar binary, the static (wasm) dir, and the DB path.
//
// Dev layout (running `electron .` from apps/desktop):
//   <repo>/apps/desktop/dist/main/index.js   ← app path is apps/desktop
//   <repo>/gridwell                          ← sidecar binary
//   <repo>/web                               ← static assets
//
// Packaged layout (Phase 5): the sidecar + web are bundled as resources
// under process.resourcesPath. We check that first, then fall back to the
// dev tree, then to env overrides.

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

export function staticDir(): string {
  const env = process.env.GRIDWELL_STATIC;
  if (env && fs.existsSync(env)) return env;

  const packaged = path.join(process.resourcesPath ?? '', 'web');
  if (fs.existsSync(packaged)) return packaged;

  return path.join(repoRoot(), 'web');
}

export function dbPath(): string {
  const env = process.env.GRIDWELL_DB;
  if (env) return env;
  // userData is the conventional per-user writable location across all
  // three OSes. SQLite lives here so a packaged, read-only app still has
  // a writable store.
  return path.join(app.getPath('userData'), 'gridwell.db');
}
