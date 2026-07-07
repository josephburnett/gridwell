import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { sweepLeakedHomes, pluginUUIDs } from './homes';

// Issue #108's sweep, unit-covered (coverage gap found in the audit): a fake
// leaked home from an "aborted run" must be removed by the start-of-run
// sweep, and only gridwell-e2e-* prefixed homes are ever touched.

test('sweepLeakedHomes removes leaked e2e homes and nothing else', () => {
  const leaked = fs.mkdtempSync(path.join(os.tmpdir(), 'gridwell-e2e-'));
  fs.writeFileSync(
    path.join(leaked, 'server.yaml'),
    'plugins:\n    - id: 0123456789abcdef0123456789abcdef\n      kind: localdb\n',
  );
  const foreign = fs.mkdtempSync(path.join(os.tmpdir(), 'gridwell-real-'));
  try {
    assert.deepEqual(pluginUUIDs(leaked), ['0123456789abcdef0123456789abcdef']);
    sweepLeakedHomes();
    assert.equal(fs.existsSync(leaked), false, 'the leaked e2e home is swept');
    assert.equal(fs.existsSync(foreign), true, 'a non-e2e dir is never touched');
  } finally {
    fs.rmSync(foreign, { recursive: true, force: true });
    fs.rmSync(leaked, { recursive: true, force: true });
  }
});
