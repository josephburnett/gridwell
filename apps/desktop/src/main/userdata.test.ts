import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as path from 'node:path';
import * as os from 'node:os';
import * as fs from 'node:fs';
import { applyUserDataOverride } from './userdata';

test('applyUserDataOverride is a no-op when GRIDWELL_HOME is absent', () => {
  const calls: Array<[string, string]> = [];
  applyUserDataOverride((n, v) => calls.push([n, v]), {});
  assert.deepEqual(calls, []);
});

test('applyUserDataOverride calls setPath for userData and sessionData', () => {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'userdata-test-'));
  try {
    const home = path.join(tmp, 'home');
    const calls: Array<[string, string]> = [];
    applyUserDataOverride((n, v) => calls.push([n, v]), { GRIDWELL_HOME: home });

    const expected = path.join(home, 'electron');
    assert.deepEqual(calls, [
      ['userData', expected],
      ['sessionData', expected],
    ]);
    // The dir must be created (mkdirSync).
    assert.ok(fs.existsSync(expected), 'electron dir was created');
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
});

test('applyUserDataOverride tolerates a setPath that throws on sessionData', () => {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'userdata-test-'));
  try {
    const home = path.join(tmp, 'home');
    const calls: string[] = [];
    applyUserDataOverride(
      (n) => {
        calls.push(n);
        if (n === 'sessionData') throw new Error('unknown path name');
      },
      { GRIDWELL_HOME: home },
    );
    assert.deepEqual(calls, ['userData', 'sessionData']);
    // Must not throw; userData was still set.
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
});
