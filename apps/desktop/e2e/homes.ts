import { spawnSync } from 'node:child_process';
import * as path from 'node:path';
import * as os from 'node:os';
import * as fs from 'node:fs';

// Shared helpers for the throwaway Gridwell homes the e2e suite creates
// (fixtures.ts seedHome) and the leak sweep that keeps an aborted run from
// polluting later ones.

// pluginUUIDs extracts every minted id from <home>/server.yaml: the node's own
// and any plugin row's. Teardown and the sweep use them to kill the tmux
// servers those ids name.
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
    // A top-level `id: <id>` or a YAML list entry `    - id: <id>`; the
    // optional dash is load-bearing, since without it the per-test tmux kill
    // matches nothing and servers leak. Both minted id shapes are valid
    // forever: 32-hex and the 7-char base36 short form. Matching only one shape
    // leaks a tmux server for every id of the other.
    const m = line.match(/^\s*-?\s*id:\s*([0-9a-f]{32}|[a-z][0-9a-z]{6})\s*$/i);
    if (m) uuids.push(m[1]);
  }
  return uuids;
}

// killTmuxServers kills the gridwell-private tmux server for each id. A node's
// home owns a tmux server on the socket "gridwell-<id>"
// (internal/node/nativelocal.go), and if a test crashes before deleting its
// shell tiles those sessions linger and interfere with the next test.
// Best-effort: a no-op when nothing is running.
export function killTmuxServers(uuids: string[]): void {
  for (const uuid of uuids) {
    spawnSync('tmux', ['-L', `gridwell-${uuid}`, 'kill-server'], { stdio: 'ignore' });
  }
}

// sweepLeakedHomes removes every throwaway e2e home a previous run left in the
// tmpdir, killing its tmux servers first, plus any stale gridwell-* tmux socket
// whose server is gone. The per-test teardown already cleans up, but a
// hard-killed worker never runs it: a teardown that exceeds the test timeout
// gets the whole worker SIGKILLed, and those leaks accumulate until suite runs
// slow to a crawl. Sweeping at the start of each run survives any kind of kill.
// Only e2e-created homes are touched, by the gridwell-e2e- mkdtemp prefix,
// never the user's real ~/.gridwell.
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

// sweepStaleSockets removes gridwell-* tmux socket files with no server behind
// them. kill-server deletes the socket only when a live server answers, so a
// SIGKILLed run leaves the file behind forever. A dead socket is probed with
// list-sessions, which fails fast without spawning a server. Two things stay
// out of scope: non-gridwell sockets, since the user's own tmux lives on
// "default", and any live gridwell-<id> server, because once its e2e home is
// gone a leaked live server is indistinguishable from the user's real desktop
// app. The per-test teardown owns killing its own servers.
function sweepStaleSockets(): void {
  // tmux's own socket-dir rule: $TMUX_TMPDIR, else /tmp, not os.tmpdir().
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
