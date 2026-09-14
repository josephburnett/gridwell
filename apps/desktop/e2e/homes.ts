import { spawnSync } from 'node:child_process';
import * as path from 'node:path';
import * as os from 'node:os';
import * as fs from 'node:fs';

// Helpers for the throwaway homes the e2e suite creates (seedHome in
// fixtures.ts) and the leak sweep that keeps an aborted run from polluting
// later ones.

// A throwaway home, named for the process that owns it. The pid rides in the
// name mkdtemp creates atomically, so a home is never for an instant a
// directory the sweep cannot attribute; that name is the one owner of "a run
// is using this home".
export function makeHome(): string {
  return fs.mkdtempSync(path.join(os.tmpdir(), `gridwell-e2e-p${process.pid}-`));
}

// Whether the process a home's name claims is still running. A name carrying
// no pid comes from a release before makeHome owned the shape, so nobody holds
// it.
function ownerAlive(name: string): boolean {
  const m = name.match(/^gridwell-e2e-p(\d+)-/);
  if (!m) return false;
  try {
    process.kill(Number(m[1]), 0);
    return true;
  } catch (err: unknown) {
    // EPERM: alive, and not ours to signal. ESRCH: gone.
    return (err as NodeJS.ErrnoException).code === 'EPERM';
  }
}

// Every minted id in <home>/server.yaml, the node's own and any plugin row's.
// Teardown and the sweep kill the tmux servers those ids name.
export function pluginUUIDs(home: string): string[] {
  const p = path.join(home, 'server.yaml');
  let src: string;
  try {
    src = fs.readFileSync(p, 'utf-8');
  } catch {
    return [];
  }
  const uuids: string[] = [];
  for (const line of src.split('\n')) {
    // A top-level `id:` or a list entry `- id:`, and both minted id shapes,
    // 32-hex and 7-char base36. Missing either leaks a tmux server.
    const m = line.match(/^\s*-?\s*id:\s*([0-9a-f]{32}|[a-z][0-9a-z]{6})\s*$/i);
    if (m) uuids.push(m[1]);
  }
  return uuids;
}

// A node's home owns a tmux server on the socket "gridwell-<id>"
// (internal/node/nativelocal.go), and a test that crashes before deleting its
// shell tiles leaves those sessions to interfere with the next test.
export function killTmuxServers(uuids: string[]): void {
  for (const uuid of uuids) {
    spawnSync('tmux', ['-L', `gridwell-${uuid}`, 'kill-server'], { stdio: 'ignore' });
  }
}

// The per-test teardown already cleans up, but a teardown that exceeds the test
// timeout gets the worker SIGKILLed and never runs, so those leaks accumulate.
// Sweeping at the start of each run survives any kind of kill. Only the
// gridwell-e2e- mkdtemp prefix is touched, never the user's real ~/.gridwell,
// and only a home no live process owns: two runs share one os.tmpdir(), so a
// sweep that took every home deleted the other run's out from under its test.
export function sweepLeakedHomes(): void {
  const tmp = os.tmpdir();
  let names: string[] = [];
  try {
    names = fs.readdirSync(tmp);
  } catch {
    return;
  }
  let swept = 0;
  for (const name of names) {
    if (!name.startsWith('gridwell-e2e-')) continue;
    if (ownerAlive(name)) continue;
    const home = path.join(tmp, name);
    killTmuxServers(pluginUUIDs(home));
    try {
      fs.rmSync(home, { recursive: true, force: true });
      swept++;
    } catch {
      // In use or already gone; leave it for the next sweep.
    }
  }
  if (swept > 0) {
    console.log(`[e2e] swept ${swept} leaked home(s) from previous aborted runs`);
  }
  sweepStaleSockets();
}

// kill-server deletes a socket only when a live server answers, so a SIGKILLed
// run leaves the file forever. A live gridwell-<id> server is left alone,
// because once its e2e home is gone it is indistinguishable from the user's
// real desktop app; the per-test teardown owns killing its own.
function sweepStaleSockets(): void {
  // tmux's own socket-dir rule: $TMUX_TMPDIR, else /tmp. Not os.tmpdir().
  const dir = path.join(process.env.TMUX_TMPDIR || '/tmp', `tmux-${process.getuid?.() ?? ''}`);
  let socks: string[] = [];
  try {
    socks = fs.readdirSync(dir);
  } catch {
    return; // no tmux dir, nothing to sweep
  }
  let removed = 0;
  for (const s of socks) {
    if (!s.startsWith('gridwell-')) continue;
    const alive = spawnSync('tmux', ['-L', s, 'list-sessions'], { stdio: 'ignore' }).status === 0;
    if (!alive) {
      try {
        fs.unlinkSync(path.join(dir, s));
        removed++;
      } catch {
        // Vanished between readdir and unlink.
      }
    }
  }
  if (removed > 0) {
    console.log(`[e2e] removed ${removed} stale tmux socket(s)`);
  }
}
