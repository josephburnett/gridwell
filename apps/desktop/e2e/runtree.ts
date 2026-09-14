import * as fs from 'node:fs';
import * as path from 'node:path';
import { makeRunDir } from './homes';

// The one owner of "the tree this run tests". A test resolves the sidecar, the
// plugin binaries and web/ at its own launch, minutes after the gate built
// them, so a rebuild in the shared checkout mid-run hands the tests that have
// not started yet a client nobody ran a gate on. The run copies the artifacts
// once, at global setup, and every launch reads that copy.

const REPO_ROOT = path.resolve(__dirname, '..', '..', '..');

// Carries the snapshot from global setup, which makes it, to the workers, which
// launch from it: Playwright forks a worker after global setup has run, so the
// worker inherits it.
const TREE_ENV = 'GRIDWELL_E2E_TREE';

// One server binary per run, under the electron shell and as a bare serve
// alike. GRIDWELL_SERVE_BIN names another build of it.
function serveName(): string {
  return process.env.GRIDWELL_SERVE_BIN || 'gridwell';
}

// What a launch reads out of the repo root: the server binary, web/, which
// holds the wasm client, and every plugin binary, which the server resolves
// beside itself (resolveBinary, internal/cli/serve.go).
function artifacts(root: string): string[] {
  const names = new Set([serveName(), 'web']);
  for (const n of fs.readdirSync(root)) {
    if (n.startsWith('gridwell-plugin-')) names.add(n);
  }
  return [...names];
}

// Copies the artifacts into one directory this run owns and publishes it. A
// missing one throws here, at setup, rather than as the first test's failure.
export function snapshotTree(root: string = REPO_ROOT): string {
  const dir = makeRunDir();
  for (const name of artifacts(root)) {
    // Copies, never links: `go build` may rewrite its output in place, and a
    // link would carry that rewrite into the snapshot.
    fs.cpSync(path.join(root, name), path.join(dir, name), { recursive: true, dereference: true });
  }
  process.env[TREE_ENV] = dir;
  return dir;
}

export function removeTree(): void {
  const dir = process.env[TREE_ENV];
  if (!dir) return;
  fs.rmSync(dir, { recursive: true, force: true });
  delete process.env[TREE_ENV];
}

// The root every launch reads. Falling back to the repo root would restore the
// coupling this module exists to cut, so a suite whose config declares no
// global setup fails instead.
export function treeRoot(): string {
  const dir = process.env[TREE_ENV];
  if (!dir) {
    throw new Error(
      `${TREE_ENV} is unset: e2e/global-setup.ts snapshots the tree under test, and the ` +
        'playwright config must declare it as globalSetup',
    );
  }
  return dir;
}

export function serveBin(): string {
  return path.join(treeRoot(), serveName());
}

function pluginDir(): string {
  return treeRoot();
}

export function staticDir(): string {
  return path.join(treeRoot(), 'web');
}

// The overrides every launch carries, so nothing resolves an artifact from the
// developer's environment. paths.ts and internal/cli/serve.go read them.
export function treeEnv(): Record<string, string> {
  return {
    GRIDWELL_SIDECAR: serveBin(),
    GRIDWELL_STATIC: staticDir(),
    GRIDWELL_PLUGIN_DIR: pluginDir(),
  };
}
