// The sidecar lifecycle's settle rules, which decide whether the app boots,
// fails fast with a cause, or hangs. A fake child process drives every path
// under `node --test`, with no Go binary, no Electron and no network, through
// the test seams in StartOptions.
import { test, type TestContext } from 'node:test';
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

function boot(child: FakeChild, silenceMs = 200, crashes: Record<string, string>[] = []) {
  return startSidecar({
    port: 4242,
    silenceMs,
    onLog: () => {},
    onCrash: (kv) => crashes.push(kv),
    spawnFn: () => child as unknown as ChildProcess,
    binaryPath: '/fake/gridwell',
    staticPath: '/fake/web',
  });
}

test('resolves on the serving banner with the ANNOUNCED address, not the requested one', async () => {
  const child = new FakeChild();
  const p = boot(child);
  // The banner arrives split across chunks and on stderr; both happen.
  child.stderr.write('gridwell: serving on 127.0');
  child.stderr.write('.0.1:9999 (static=/x plugins=1 federation=/tmp/gw home/federation.sock)\n');
  const sc = await p;
  assert.equal(sc.origin, 'http://127.0.0.1:9999', 'the origin comes from the banner (server.yaml web.bind may override the request)');
  sc.stop();
  assert.ok(child.killed, 'stop() kills the child');
  assert.deepEqual(child.signals, ['SIGTERM']);
});

test('passes the banner auth token through so the window can authenticate', async () => {
  const child = new FakeChild();
  const p = boot(child);
  const token = 'b'.repeat(64);
  child.stdout.write(`gridwell: serving on 127.0.0.1:9999 (static=/x plugins=1 auth=${token} federation=/tmp/gw home/federation.sock)\n`);
  const sc = await p;
  assert.equal(sc.auth, token, 'index.ts pre-sets this as the auth cookie — no prompt in the desktop app');
  sc.stop();
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

test('gives up on SILENCE and kills the child when nothing is announced', async () => {
  const child = new FakeChild();
  await assert.rejects(boot(child, 50), /went silent for 50ms/);
  assert.ok(child.killed, 'the hung child is terminated, not leaked');
});

// The silence window is a timer, so the two tests that race it drive the
// clock by hand: a real 30ms sleep against a real 40ms window loses under load.
async function speakAfter(t: TestContext, child: FakeChild, ms: number, line: string) {
  t.mock.timers.tick(ms);
  child.stdout.write(`${line}\n`);
  await new Promise((r) => setImmediate(r)); // the line reaches the reader
}

test('a slow but TALKING boot is never killed: every line re-arms the window', async (t) => {
  // A long boot, where each step announces itself and then takes longer than
  // the whole window before the next one. Only silence may end a boot.
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const child = new FakeChild();
  const p = boot(child, 40);
  for (const line of [
    'gridwell: home: scratch cleanup removed 41 ephemeral tile(s)',
    'gridwell: home: orphan cleanup killed 3 stale tmux(es)',
    'gridwell: plugin fs (fs) respawned',
  ]) {
    await speakAfter(t, child, 30, line); // under the window, every time
  }
  await speakAfter(t, child, 30, 'gridwell: serving on 127.0.0.1:9999 (static=/x plugins=1)');
  const sc = await p; // well past 40ms, and alive throughout
  assert.equal(sc.origin, 'http://127.0.0.1:9999');
  assert.equal(child.killed, false, 'a talking process must never be killed for being slow');
  sc.stop();
});

test('silence AFTER progress still ends the boot: the window re-arms, it does not vanish', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const child = new FakeChild();
  const p = boot(child, 40);
  await speakAfter(t, child, 20, 'gridwell: home: scratch cleanup removed 41 ephemeral tile(s)');
  t.mock.timers.tick(40);
  await assert.rejects(p, /went silent for 40ms/);
  assert.ok(child.killed, 'a hung process is still terminated, however far it got');
});

test('a late banner after settle does not double-resolve or unkill', async () => {
  const child = new FakeChild();
  await assert.rejects(boot(child, 50), /went silent/);
  child.stderr.write('gridwell: serving on 127.0.0.1:9999\n'); // ignored: settled
  assert.ok(child.killed);
});

test('the exit rejection carries the server\'s own diagnostics, not just a code', async () => {
  const child = new FakeChild();
  const p = boot(child);
  child.stderr.write('serve: no database at /x/db — run gridwell init\n');
  child.emit('exit', 1, null);
  await assert.rejects(p, /no database at \/x\/db/);
});

test('"already serving" resolves EXTERNAL: connect, never watch, never kill', async () => {
  const child = new FakeChild();
  const p = boot(child);
  const token = 'c'.repeat(64);
  child.stdout.write(
    `gridwell: already serving on 127.0.0.1:7001 (static=embedded plugins=2 auth=${token} federation=/tmp/gw home/federation.sock)\n`,
  );
  const sc = await p;
  assert.equal(sc.external, true);
  assert.equal(sc.origin, 'http://127.0.0.1:7001');
  assert.equal(sc.auth, token, 'the RUNNING holder\'s token authenticates this window');
  sc.stop();
  assert.equal(child.killed, false, 'stop() must never signal toward an external server');
});

test('--no-server runs `status` and rejects clearly when nothing is running', async () => {
  const argvs: string[][] = [];
  const child = new FakeChild();
  const p = startSidecar({
    silenceMs: 200,
    onLog: () => {},
    noServer: true,
    spawnFn: (_bin, args) => {
      argvs.push(args);
      return child as unknown as ChildProcess;
    },
    binaryPath: '/fake/gridwell',
  });
  await new Promise((r) => setTimeout(r, 10)); // spawn happens after an internal await
  assert.deepEqual(argvs[0], ['status'], '--no-server must never start a server');
  child.stdout.write('gridwell: not serving\n');
  await assert.rejects(p, /no server is running/);
});

test('--no-server connects to a running server via the status banner', async () => {
  const child = new FakeChild();
  const p = startSidecar({
    silenceMs: 200,
    onLog: () => {},
    noServer: true,
    spawnFn: () => child as unknown as ChildProcess,
    binaryPath: '/fake/gridwell',
  });
  child.stdout.write('gridwell: already serving on 127.0.0.1:10010 (static=embedded plugins=2 federation=/tmp/gw home/federation.sock)\n');
  const sc = await p;
  assert.equal(sc.external, true);
  assert.equal(sc.origin, 'http://127.0.0.1:10010');
});

// The node's ring dies with it and a panic's stack is written straight to
// stderr, so an exit nobody asked for is the one moment the tail must be kept.
test('an exit nobody asked for is a crash; a stop, a clean shutdown and a probe are not', async () => {
  const ready = 'gridwell: serving on 127.0.0.1:9999 (static=/x plugins=1)\n';

  const panicked = new FakeChild();
  const crashes: Record<string, string>[] = [];
  const p = boot(panicked, 200, crashes);
  panicked.stdout.write(ready);
  await p;
  panicked.emit('exit', 2, null);
  assert.deepEqual(crashes, [{ code: '2', signal: '' }]);

  const killed = new FakeChild();
  const k: Record<string, string>[] = [];
  const pk = boot(killed, 200, k);
  killed.stdout.write(ready);
  await pk;
  killed.emit('exit', null, 'SIGKILL');
  assert.deepEqual(k, [{ code: '', signal: 'SIGKILL' }], 'a kill from outside is a crash');

  const stopped = new FakeChild();
  const s: Record<string, string>[] = [];
  const ps = boot(stopped, 200, s);
  stopped.stdout.write(ready);
  (await ps).stop();
  stopped.emit('exit', null, 'SIGTERM');
  assert.deepEqual(s, [], 'stop() asked for that exit');

  const clean = new FakeChild();
  const c: Record<string, string>[] = [];
  const pc = boot(clean, 200, c);
  clean.stdout.write(ready);
  await pc;
  clean.emit('exit', 0, null);
  assert.deepEqual(c, [], 'a node that shut itself down on a signal exits 0');

  const probe = new FakeChild();
  const e: Record<string, string>[] = [];
  const pe = boot(probe, 200, e);
  probe.stdout.write('gridwell: already serving on 127.0.0.1:7001 (static=embedded plugins=2 federation=/x)\n');
  await pe;
  probe.emit('exit', 1, null);
  assert.deepEqual(e, [], 'the probe of an external server exits by design');

  const early = new FakeChild();
  const b: Record<string, string>[] = [];
  const pb = boot(early, 200, b);
  early.stderr.write('panic: boom\n');
  early.emit('exit', 2, null);
  await assert.rejects(pb);
  assert.deepEqual(b, [{ code: '2', signal: '' }], 'a death before the banner is a crash too');
});
