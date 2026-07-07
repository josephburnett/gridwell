import { spawnSync } from 'node:child_process';
import * as path from 'node:path';
import * as os from 'node:os';
import * as fs from 'node:fs';

// Shared helpers for the throwaway Gridwell homes the e2e suite creates
// (fixtures.ts seedHome) and the leak sweep that keeps aborted runs from
// polluting later ones (issue #108).

// pluginUUIDs extracts the plugin UUIDs from <home>/server.yaml. Used by
// teardown and the sweep to kill the per-plugin tmux servers.
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
    // YAML line: `  id: <uuid>`
    const m = line.match(/^\s*id:\s*([0-9a-f]{32})\s*$/i);
    if (m) uuids.push(m[1]);
  }
  return uuids;
}

// killTmuxServers kills the gridwell-private tmux server for each plugin UUID.
// Each localdb plugin owns a tmux server on socket "gridwell-<uuid>"; if a
// test crashes before deleting shell tiles those sessions linger and can
// interfere with the next test. Best-effort: no-op if not running.
export function killTmuxServers(uuids: string[]): void {
  for (const uuid of uuids) {
    spawnSync('tmux', ['-L', `gridwell-${uuid}`, 'kill-server'], { stdio: 'ignore' });
  }
}

// sweepLeakedHomes removes every throwaway e2e home a PREVIOUS run left in
// the tmpdir — killing its tmux servers first — plus any stale gridwell-*
// tmux sockets whose server is gone. The per-test teardown already cleans up
//, but a HARD-KILLED worker (a teardown that exceeds the test
// timeout gets the whole worker SIGKILLed) never runs it; those leaks
// accumulated until suite runs degraded from 4 to 10 minutes (issue #108).
// Sweeping at the START of each run is robust to any kind of kill: the next
// run cleans up, and only e2e-created homes (the gridwell-e2e- mkdtemp
// prefix) are ever touched — never the user's real ~/.gridwell.
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
      // in use or already gone — leave it for the next sweep
    }
  }
  if (swept > 0) {
    console.log(`[e2e] swept ${swept} leaked home(s) from previous aborted runs`);
  }
}
