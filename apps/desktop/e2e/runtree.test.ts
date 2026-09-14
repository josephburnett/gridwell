import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { snapshotTree, removeTree, treeRoot, serveBin, staticDir, treeEnv } from './runtree';

// The snapshot is what stands between a run and a rebuild in the checkout it
// was launched from: `make check-e2e` builds, then tests launch for minutes
// afterwards, and a peer's build lands in the same files.

// These tests own the published snapshot while they run, so they hide any that
// a suite running beside them published — removeTree deletes whatever the
// variable names, and this process must never name another run's.
function withNoTree(body: () => void): void {
  const saved = process.env.GRIDWELL_E2E_TREE;
  delete process.env.GRIDWELL_E2E_TREE;
  try {
    body();
  } finally {
    removeTree();
    if (saved === undefined) delete process.env.GRIDWELL_E2E_TREE;
    else process.env.GRIDWELL_E2E_TREE = saved;
  }
}

// A repo root as `make build` leaves one: the server binary, a plugin binary
// beside it, and web/ holding the wasm client.
function fakeRoot(bytes: string): string {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'runtree-test-'));
  fs.writeFileSync(path.join(root, 'gridwell'), `server ${bytes}`);
  fs.writeFileSync(path.join(root, 'gridwell-plugin-fake'), `plugin ${bytes}`);
  fs.mkdirSync(path.join(root, 'web'));
  fs.writeFileSync(path.join(root, 'web', 'gridwell.wasm'), `client ${bytes}`);
  return root;
}

test('a rebuild under a running suite does not change what the suite tests', () => {
  withNoTree(() => {
    const root = fakeRoot('built');
    try {
      const snap = snapshotTree(root);

      // The peer rebuild: every artifact rewritten in place, as `go build -o`
      // does, while the run that pinned them is still launching tests.
      fs.writeFileSync(path.join(root, 'gridwell'), 'server rebuilt');
      fs.writeFileSync(path.join(root, 'gridwell-plugin-fake'), 'plugin rebuilt');
      fs.writeFileSync(path.join(root, 'web', 'gridwell.wasm'), 'client rebuilt');

      const read = (...p: string[]) => fs.readFileSync(path.join(snap, ...p), 'utf8');
      assert.equal(read('gridwell'), 'server built');
      assert.equal(read('gridwell-plugin-fake'), 'plugin built');
      assert.equal(read('web', 'gridwell.wasm'), 'client built');

      // And every launch reads the snapshot: the sidecar the Electron fixture
      // names, the binary a bare serve spawns, the static dir it serves, and
      // the directory the server resolves plugin binaries from.
      assert.equal(treeRoot(), snap);
      assert.equal(serveBin(), path.join(snap, 'gridwell'));
      assert.equal(staticDir(), path.join(snap, 'web'));
      assert.deepEqual(treeEnv(), {
        GRIDWELL_SIDECAR: path.join(snap, 'gridwell'),
        GRIDWELL_STATIC: path.join(snap, 'web'),
        GRIDWELL_PLUGIN_DIR: snap,
      });

      removeTree();
      assert.equal(fs.existsSync(snap), false, 'teardown takes the snapshot with it');
    } finally {
      fs.rmSync(root, { recursive: true, force: true });
    }
  });
});

// Falling back to the checkout would put the class back silently.
test('a launch with no snapshot fails rather than reading the checkout', () => {
  withNoTree(() => {
    assert.throws(() => treeRoot(), /GRIDWELL_E2E_TREE/);
  });
});
