import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { snapshotDir, restoreDir, sessionStateEntry } from './session';

// Issue #123: the session blob carries SESSION STATE, never Chromium's
// regenerable machinery. A real logged-in partition measured 238MB of which
// 227MB was disk cache — snapshotting it all ballooned the blob until every
// dehydrate failed. The allowlist makes that growth class impossible.

test('sessionStateEntry allowlists state roots and their sqlite siblings', () => {
  for (const name of ['Cookies', 'Cookies-journal', 'Cookies-wal', 'Local Storage', 'Session Storage', 'IndexedDB', 'WebStorage']) {
    assert.equal(sessionStateEntry(name), true, name);
  }
  for (const name of ['Cache', 'Code Cache', 'GPUCache', 'DawnWebGPUCache', 'Service Worker', 'File System', 'Shared Dictionary', 'Preferences', 'CookiesFake']) {
    assert.equal(sessionStateEntry(name), false, name);
  }
});

test('snapshotDir captures only session state; restoreDir round-trips it', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'gw-session-test-'));
  const write = (rel: string, content: string) => {
    const p = path.join(dir, rel);
    fs.mkdirSync(path.dirname(p), { recursive: true });
    fs.writeFileSync(p, content);
  };
  write('Cookies', 'cookie-db');
  write('Cookies-journal', 'j');
  write('Local Storage/leveldb/000003.log', 'ls-data');
  write('IndexedDB/https_example.com_0.indexeddb.leveldb/CURRENT', 'idb');
  write('Session Storage/LOG', 'ss');
  // The regenerable bulk that must NOT ride the blob:
  write('Cache/Cache_Data/f_000001', 'x'.repeat(4096));
  write('Code Cache/js/index', 'x'.repeat(4096));
  write('GPUCache/data_0', 'x');
  write('Service Worker/CacheStorage/abc/index', 'x'.repeat(2048));

  const snap = snapshotDir(dir);
  const keys = Object.keys(snap).sort();
  assert.deepEqual(keys, [
    'Cookies',
    'Cookies-journal',
    'IndexedDB/https_example.com_0.indexeddb.leveldb/CURRENT',
    'Local Storage/leveldb/000003.log',
    'Session Storage/LOG',
  ]);

  // Round trip into a fresh dir: bytes identical.
  const dst = fs.mkdtempSync(path.join(os.tmpdir(), 'gw-session-restore-'));
  restoreDir(dst, snap);
  assert.equal(fs.readFileSync(path.join(dst, 'Cookies'), 'utf8'), 'cookie-db');
  assert.equal(
    fs.readFileSync(path.join(dst, 'Local Storage/leveldb/000003.log'), 'utf8'),
    'ls-data',
  );

  fs.rmSync(dir, { recursive: true, force: true });
  fs.rmSync(dst, { recursive: true, force: true });
});
