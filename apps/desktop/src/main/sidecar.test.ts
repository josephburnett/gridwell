// Unit tests for the sidecar lifecycle (issue #10): the settle rules that
// decide whether the app boots, fails fast with a cause, or hangs. A fake
// child process (EventEmitter + PassThrough stdio) drives every path under
// `node --test` — no Go binary, no Electron, no network. The real spawn and
// path resolution stay untouched (test seams in StartOptions).
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { PassThrough } from 'node:stream';
import type { ChildProcess } from 'node:child_process';
import { startSidecar } from './sidecar';

class FakeChild extends EventEmitter {
  stdout = new PassThrough();
  stderr = new PassThrough();
  killed = false;
  signals: string[] = [];
  kill(sig?: string): boolean {
    this.killed = true;
    this.signals.push(sig ?? 'SIGTERM');
    return true;
  }
}

function boot(child: FakeChild, timeoutMs = 200) {
  return startSidecar({
    port: 4242,
    timeoutMs,
    onLog: () => {},
    spawnFn: () => child as unknown as ChildProcess,
    binaryPath: '/fake/gridwell',
    staticPath: '/fake/web',
  });
}

test('resolves on the serving banner with the ANNOUNCED address, not the requested one', async () => {
  const child = new FakeChild();
  const p = boot(child);
  // The banner arrives split across chunks and on stderr — both real.
  child.stderr.write('gridwell: serving on 127.0');
  child.stderr.write('.0.1:9999 (static=/x plugins=1)\n');
  const sc = await p;
  assert.equal(sc.port, 9999, 'port comes from the banner (server.yaml bind: may override the request)');
  assert.equal(sc.origin, 'http://127.0.0.1:9999');
  sc.stop();
  assert.ok(child.killed, 'stop() kills the child');
  assert.deepEqual(child.signals, ['SIGTERM']);
});

test('rejects with the exit cause when the child dies before announcing', async () => {
  const child = new FakeChild();
  const p = boot(child);
  child.stdout.write('gridwell: some startup log\n');
  child.emit('exit', 3, null);
  await assert.rejects(p, /exited before ready \(code=3/);
});

test('rejects immediately on a spawn error (not the generic timeout)', async () => {
  const child = new FakeChild();
  const p = boot(child);
  child.emit('error', new Error('ENOEXEC'));
  await assert.rejects(p, /ENOEXEC/);
});

test('times out and kills the child when nothing is announced', async () => {
  const child = new FakeChild();
  await assert.rejects(boot(child, 50), /did not report ready within 50ms/);
  assert.ok(child.killed, 'the hung child is terminated, not leaked');
});

test('a late banner after settle does not double-resolve or unkill', async () => {
  const child = new FakeChild();
  await assert.rejects(boot(child, 50), /did not report ready/);
  child.stderr.write('gridwell: serving on 127.0.0.1:9999\n'); // ignored: settled
  assert.ok(child.killed);
});
