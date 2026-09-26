import { spawnSync } from 'node:child_process';
import * as path from 'node:path';
import * as os from 'node:os';
import * as fs from 'node:fs';
import { TmuxProc, sweptHomes, tmuxToKill } from './sweep';

// Helpers for the throwaway directories the e2e suite creates — the seeded
// homes (seedHome in fixtures.ts) and the run's artifact snapshot (runtree.ts)
// — and the leak sweep that keeps an aborted run from polluting later ones.

// A throwaway directory, named for the process that owns it. The pid rides in
// the name mkdtemp creates atomically, so such a directory is never for an
// instant one the sweep cannot attribute; that name is the one owner of "a run
// is using this".
export function makeRunDir(): string {
  return fs.mkdtempSync(path.join(os.tmpdir(), `gridwell-e2e-p${process.pid}-`));
}

// A throwaway home, for one test's node.
export function makeHome(): string {
  return makeRunDir();
}

// The environment that points a node at its home. Its temp dir rides inside,
// so what the node mints there (tmux's config dir and socket, go-plugin's
// sockets) goes when the home goes, and its tmux server's argv names the home
// it belongs to (tmuxToKill).
export function homeEnv(home: string): Record<string, string> {
  const tmp = path.join(home, 'tmp');
  fs.mkdirSync(tmp, { recursive: true });
  return { GRIDWELL_HOME: home, TMPDIR: tmp, TMUX_TMPDIR: tmp };
}

function alive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch (err: unknown) {
    // EPERM: alive, and not ours to signal. ESRCH: gone.
    return (err as NodeJS.ErrnoException).code === 'EPERM';
  }
}

// Every tmux process on the box with its argv, read from /proc; a host
// without one lists none.
function tmuxProcs(): TmuxProc[] {
  let pids: string[];
  try {
    pids = fs.readdirSync('/proc').filter((n) => /^\d+$/.test(n));
  } catch {
    return [];
  }
  const procs: TmuxProc[] = [];
  for (const pid of pids) {
    try {
      if (!fs.readFileSync(`/proc/${pid}/comm`, 'utf-8').startsWith('tmux')) continue;
      const args = fs.readFileSync(`/proc/${pid}/cmdline`, 'utf-8').split('\0').filter(Boolean);
      procs.push({ pid: Number(pid), args });
    } catch {
      // Exited mid-scan.
    }
  }
  return procs;
}

// Returns once every killed server has exited, so nothing the caller removes
// next is still in use.
function killTmux(procs: TmuxProc[], homes: ReadonlySet<string>): void {
  const pids = tmuxToKill(procs, homes);
  for (const pid of pids) {
    try {
      process.kill(pid, 'SIGTERM'); // tmux's own kill-server
    } catch {
      // already gone
    }
  }
  const deadline = Date.now() + 3_000;
  const tick = new Int32Array(new SharedArrayBuffer(4));
  while (pids.some(alive) && Date.now() < deadline) Atomics.wait(tick, 0, 0, 20);
}

// Removes a home and everything it owns outside itself: the tmux servers its
// node started. The one remover of a home a test is done with.
export function removeHome(home: string): void {
  killTmux(tmuxProcs(), new Set([home]));
  fs.rmSync(home, { recursive: true, force: true });
}

// The per-test teardown already cleans up, but a teardown that exceeds the test
// timeout gets the worker SIGKILLed and never runs, so those leaks accumulate.
// Global setup and teardown both sweep, which survives any kind of kill. Only
// the gridwell-e2e- mkdtemp prefix is touched, never the user's real
// ~/.gridwell, and only a directory no live process owns: two runs share one
// os.tmpdir(), so a sweep that took every home deleted the other run's out from
// under its test. `mine` is the runner's pid at its own teardown (sweep.ts).
export function sweepLeakedHomes(mine?: number): void {
  const tmp = os.tmpdir();
  let names: string[] = [];
  try {
    names = fs.readdirSync(tmp);
  } catch {
    return;
  }
  const procs = tmuxProcs();
  const homes = sweptHomes(tmp, names, procs, alive, mine);
  killTmux(procs, homes);
  let swept = 0;
  for (const home of homes) {
    if (!fs.existsSync(home)) continue;
    try {
      fs.rmSync(home, { recursive: true, force: true });
      swept++;
    } catch {
      // In use or already gone; leave it for the next sweep.
    }
  }
  if (swept > 0) {
    console.log(`[e2e] swept ${swept} leaked run dir(s)`);
  }
  sweepStaleSockets();
}

// kill-server deletes a socket only when a live server answers, so a SIGKILLed
// run leaves the file forever. A live server is left alone: the home its argv
// names decides whether it goes (tmuxToKill).
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
