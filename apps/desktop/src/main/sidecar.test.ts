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
  child.stderr.write('.0.1:9999 (static=/x plugins=1 federation=/tmp/gw home/federation.sock)\n');
  const sc = await p;
  assert.equal(sc.port, 9999, 'port comes from the banner (server.yaml web.bind may override the request)');
  assert.equal(sc.origin, 'http://127.0.0.1:9999');
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
  assert.equal(sc.dialAddr, 'unix:/tmp/gw home/federation.sock', 'the shell relay dials the federation socket, not the web address');
  assert.equal(sc.auth, token, 'the RUNNING holder\'s token authenticates this window');
  sc.stop();
  assert.equal(child.killed, false, 'stop() must never signal toward an external server');
});

test('first run: "no config" exits → gridwell init → one respawn → ready', async () => {
  const children: FakeChild[] = [];
  const argvs: string[][] = [];
  const p = startSidecar({
    port: 4242,
    timeoutMs: 500,
    onLog: () => {},
    spawnFn: (_bin, args) => {
      argvs.push(args);
      const c = new FakeChild();
      children.push(c);
      return c as unknown as ChildProcess;
    },
    binaryPath: '/fake/gridwell',
  });
  children[0].stderr.write('serve: no config at /h/server.yaml; run `gridwell init --kind local --name <name>` to create one\n');
  children[0].emit('exit', 1, null);
  await new Promise((r) => setTimeout(r, 10)); // let the init child spawn
  assert.deepEqual(argvs[1], ['init', '--kind', 'local', '--name', 'home']);
  children[1].emit('exit', 0, null);
  await new Promise((r) => setTimeout(r, 10)); // let the retry serve spawn
  assert.equal(argvs[2][0], 'serve', 'after init, serve is retried once');
  children[2].stdout.write('gridwell: serving on 127.0.0.1:9999 (static=embedded plugins=1 federation=/tmp/gw home/federation.sock)\n');
  const sc = await p;
  assert.equal(sc.external, false);
  assert.equal(sc.port, 9999);
});

test('first run retry is ONE-SHOT: a second no-config surfaces instead of looping', async () => {
  const children: FakeChild[] = [];
  const p = startSidecar({
    port: 4242,
    timeoutMs: 500,
    onLog: () => {},
    spawnFn: () => {
      const c = new FakeChild();
      children.push(c);
      return c as unknown as ChildProcess;
    },
    binaryPath: '/fake/gridwell',
  });
  children[0].stderr.write('serve: no config at /h/server.yaml; run `gridwell init` to create one\n');
  children[0].emit('exit', 1, null);
  await new Promise((r) => setTimeout(r, 10));
  children[1].emit('exit', 0, null); // init "succeeds"...
  await new Promise((r) => setTimeout(r, 10));
  children[2].stderr.write('serve: no config at /h/server.yaml; run `gridwell init` to create one\n');
  children[2].emit('exit', 1, null); // ...but serve STILL has no config
  await assert.rejects(p, /exited before ready/);
  assert.equal(children.length, 3, 'no second init attempt');
});

test('--no-server runs `status` and rejects clearly when nothing is running', async () => {
  const argvs: string[][] = [];
  const child = new FakeChild();
  const p = startSidecar({
    timeoutMs: 200,
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
    timeoutMs: 200,
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
