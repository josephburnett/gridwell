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

// staticDir is the OVERRIDE only (null = none): the gridwell binary embeds
// the web client (web/embed.go), so the server serves it with no --static
// at all — packaged and dev alike. GRIDWELL_STATIC remains the pin for the
// e2e harness (and anyone iterating on web/ without rebuilding the binary).
export function staticDir(): string | null {
  const env = process.env.GRIDWELL_STATIC;
  if (env && fs.existsSync(env)) return env;
  return null;
}

// dataProtoPath resolves the ONE gridwell proto definition (the shell
// transport's gRPC client loads it at runtime — no generated JS copy that
// could drift from the .proto). Same resolution order as the sidecar binary:
// env override, packaged resources, dev tree.
export function dataProtoPath(): string {
	const env = process.env.GRIDWELL_PROTO;
	if (env && fs.existsSync(env)) return env;

	const packaged = path.join(process.resourcesPath ?? '', 'data.proto');
	if (fs.existsSync(packaged)) return packaged;

	return path.join(repoRoot(), 'api', 'gridwell', 'v1', 'data.proto');
}

// The DB path is no longer resolved here: the Go server derives each plugin's
// DB from its id under the Gridwell home (GRIDWELL_HOME, else ~/.gridwell), so
// there is nothing for the Electron main process to compute or pass through.
