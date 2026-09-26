import { test } from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { makeHome, homeEnv, removeHome, sweepLeakedHomes } from './homes';

// The start-of-run sweep. A fake leaked home from an aborted run must be
// removed, only gridwell-e2e-* prefixed homes are ever touched, and a home a
// live run still owns must survive.

// The sweep derives the two directories it walks from the environment:
// os.tmpdir() for homes, $TMUX_TMPDIR for sockets. Redirecting both to one
// mkdtemp root gives this process its own copy of the tree the production code
// walks, so two runs on one box neither see nor delete each other's fixtures.
function withTmpRoot(body: () => void): void {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'homes-test-'));
  const saved = {
    TMPDIR: process.env.TMPDIR,
    TMP: process.env.TMP,
    TEMP: process.env.TEMP,
    TMUX_TMPDIR: process.env.TMUX_TMPDIR,
  };
  for (const k of Object.keys(saved)) process.env[k] = root;
  try {
    assert.equal(os.tmpdir(), root, 'the sweep must walk this run\'s own root');
    body();
  } finally {
    for (const [k, v] of Object.entries(saved)) {
      if (v === undefined) delete process.env[k];
      else process.env[k] = v;
    }
    fs.rmSync(root, { recursive: true, force: true });
  }
}

test('sweepLeakedHomes removes leaked e2e homes and nothing else', () => {
  withTmpRoot(() => {
    // The name shape a release before makeHome left behind: no owner, so it is
    // nobody's and must go.
    const leaked = fs.mkdtempSync(path.join(os.tmpdir(), 'gridwell-e2e-'));
    const foreign = fs.mkdtempSync(path.join(os.tmpdir(), 'gridwell-real-'));
    // Stale-socket sweep fixtures in tmux's socket dir ($TMUX_TMPDIR, else /tmp).
    // A dead gridwell-* socket is a plain file no server answers on and must be
    // removed; a non-gridwell name must never be touched.
    const sockDir = path.join(process.env.TMUX_TMPDIR || '/tmp', `tmux-${process.getuid?.() ?? ''}`);
    fs.mkdirSync(sockDir, { recursive: true, mode: 0o700 });
    const deadSock = path.join(sockDir, 'gridwell-zz9dead');
    const foreignSock = path.join(sockDir, 'homes-test-foreign');
    fs.writeFileSync(deadSock, '');
    fs.writeFileSync(foreignSock, '');
    sweepLeakedHomes();
    assert.equal(fs.existsSync(leaked), false, 'the leaked e2e home is swept');
    assert.equal(fs.existsSync(foreign), true, 'a non-e2e dir is never touched');
    assert.equal(fs.existsSync(deadSock), false, 'a dead gridwell-* socket is removed');
    assert.equal(fs.existsSync(foreignSock), true, 'a non-gridwell socket is never touched');
  });
});

// A second `npx playwright test` on the same box runs its global setup while
// the first run's test is mid-flight. Sweeping that test's home out from under
// it leaves a renderer anchored on a home the fixture can no longer read — the
// `gw` fixture's ENOENT on web-password (docs/flake-ledger.md).
test('sweepLeakedHomes spares a home whose owner is alive', () => {
  withTmpRoot(() => {
    const live = makeHome();
    // A pid already reaped: what a SIGKILLed worker leaves behind, and the
    // guarantee the sweep exists for.
    const gone = spawnSync(process.execPath, ['-e', '']).pid;
    const abandoned = fs.mkdtempSync(path.join(os.tmpdir(), `gridwell-e2e-p${gone}-`));
    sweepLeakedHomes();
    assert.equal(fs.existsSync(live), true, "a live owner's home survives another run's sweep");
    assert.equal(fs.existsSync(abandoned), false, "a dead owner's home is swept");
  });
});

const hasTmux = spawnSync('tmux', ['-V'], { stdio: 'ignore' }).status === 0;

// A tmux server the way a node under test starts one (homeEnv, tmux.New):
// detached, outliving whoever spawned it, its config inside the home. Returns
// whether it still runs.
function startShellServer(home: string): () => boolean {
  const env = { ...process.env, ...homeEnv(home) };
  const conf = path.join(env.TMPDIR!, 'gridwell-tmux-gridwell-k3x9m2q', 'tmux.conf');
  fs.mkdirSync(path.dirname(conf), { recursive: true });
  fs.writeFileSync(conf, '');
  const sock = `homes-test-${process.pid}-${Math.random().toString(36).slice(2, 8)}`;
  const r = spawnSync('tmux', ['-L', sock, '-f', conf, 'new-session', '-d', 'sleep 600'], { env });
  assert.equal(r.status, 0, `tmux did not start: ${r.stderr}`);
  // By pid, because a removed home takes the socket with it.
  const pid = Number(spawnSync('tmux', ['-L', sock, 'display-message', '-p', '#{pid}'], { env, encoding: 'utf-8' }).stdout);
  assert.ok(pid > 0, 'tmux server pid');
  return () => {
    try {
      process.kill(pid, 0);
      return true;
    } catch {
      return false;
    }
  };
}

// A run whose home was removed without its shells, by a fixture that never
// killed them or by a teardown that raced a respawn, leaves a server naming a
// directory that no longer exists. The sweep finds it by the home it names.
test('sweepLeakedHomes kills the tmux servers of an abandoned home, gone or not', { skip: !hasTmux }, () => {
  withTmpRoot(() => {
    const gone = spawnSync(process.execPath, ['-e', '']).pid;
    const present = fs.mkdtempSync(path.join(os.tmpdir(), `gridwell-e2e-p${gone}-`));
    const removed = path.join(os.tmpdir(), `gridwell-e2e-p${gone}-removed`);
    const live = makeHome();
    const presentUp = startShellServer(present);
    const removedUp = startShellServer(removed);
    const liveUp = startShellServer(live);
    fs.rmSync(removed, { recursive: true, force: true });
    try {
      sweepLeakedHomes();
      assert.equal(presentUp(), false, "an abandoned home's server is killed");
      assert.equal(removedUp(), false, 'a server naming an already-removed abandoned home is killed');
      assert.equal(liveUp(), true, "a live run's server survives another run's sweep");
    } finally {
      removeHome(live);
    }
    assert.equal(liveUp(), false, 'removeHome kills the servers its home owns');
    assert.equal(fs.existsSync(live), false);
  });
});

// A scratch directory minted anywhere but makeRunDir carries no owner, so no
// sweep can attribute it, and one minted at module scope leaks on every load
// of its file, including loads that run none of its tests.
test('every e2e scratch directory is minted by makeRunDir', () => {
  const offenders: string[] = [];
  for (const dir of ['e2e', 'e2e-web']) {
    const abs = path.resolve(__dirname, '..', dir);
    for (const f of fs.readdirSync(abs)) {
      if (!f.endsWith('.ts') || f.endsWith('.test.ts') || f === 'homes.ts') continue;
      if (/\bmkdtemp(Sync)?\(/.test(fs.readFileSync(path.join(abs, f), 'utf-8'))) offenders.push(`${dir}/${f}`);
    }
  }
  assert.deepEqual(offenders, []);
});
