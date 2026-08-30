import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { sweepLeakedHomes, pluginUUIDs } from './homes';

// The start-of-run sweep, unit-covered: a fake leaked home from an aborted run
// must be removed, and only gridwell-e2e-* prefixed homes are ever touched.

test('sweepLeakedHomes removes leaked e2e homes and nothing else', () => {
  const leaked = fs.mkdtempSync(path.join(os.tmpdir(), 'gridwell-e2e-'));
  // Both minted id shapes: 32-hex and the 7-char base36 short form. The regex
  // must find each, or that id's tmux server leaks.
  fs.writeFileSync(
    path.join(leaked, 'server.yaml'),
    'plugins:\n    - id: 0123456789abcdef0123456789abcdef\n      kind: localdb\n    - id: k3x9m2q\n      kind: localdb\n',
  );
  const foreign = fs.mkdtempSync(path.join(os.tmpdir(), 'gridwell-real-'));
  // Stale-socket sweep fixtures in tmux's socket dir ($TMUX_TMPDIR, else /tmp).
  // A dead gridwell-* socket is a plain file no server answers on and must be
  // removed; a non-gridwell name must never be touched.
  const sockDir = path.join(process.env.TMUX_TMPDIR || '/tmp', `tmux-${process.getuid?.() ?? ''}`);
  fs.mkdirSync(sockDir, { recursive: true });
  const deadSock = path.join(sockDir, 'gridwell-zz9dead');
  const foreignSock = path.join(sockDir, 'homes-test-foreign');
  fs.writeFileSync(deadSock, '');
  fs.writeFileSync(foreignSock, '');
  try {
    assert.deepEqual(pluginUUIDs(leaked), ['0123456789abcdef0123456789abcdef', 'k3x9m2q']);
    sweepLeakedHomes();
    assert.equal(fs.existsSync(leaked), false, 'the leaked e2e home is swept');
    assert.equal(fs.existsSync(foreign), true, 'a non-e2e dir is never touched');
    assert.equal(fs.existsSync(deadSock), false, 'a dead gridwell-* socket is removed');
    assert.equal(fs.existsSync(foreignSock), true, 'a non-gridwell socket is never touched');
  } finally {
    fs.rmSync(foreign, { recursive: true, force: true });
    fs.rmSync(leaked, { recursive: true, force: true });
    fs.rmSync(deadSock, { force: true });
    fs.rmSync(foreignSock, { force: true });
  }
});
