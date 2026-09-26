import * as path from 'node:path';

// The pure decisions of the leak sweep in homes.ts: which run directories are
// nobody's, and which tmux servers go with them.

// The pid a run directory's name claims (makeRunDir), or null for a name
// carrying none: a release before makeRunDir owned the shape, so nobody holds
// it.
export function runOwner(name: string): number | null {
  const m = name.match(/^gridwell-e2e-p(\d+)-/);
  return m ? Number(m[1]) : null;
}

// Whether a directory in the run root is a run directory no live process
// owns. `mine` is a pid whose directories count as finished although it is
// running: the runner itself, at its own global teardown.
export function abandoned(name: string, alive: (pid: number) => boolean, mine?: number): boolean {
  if (!name.startsWith('gridwell-e2e-')) return false;
  const pid = runOwner(name);
  if (pid == null) return true;
  return pid === mine || !alive(pid);
}

// A tmux process and its argv. A node starts tmux with `-f
// <TMPDIR>/gridwell-tmux-<socket>/tmux.conf` (internal/local/tmux.New), and
// homeEnv puts TMPDIR inside the home, so the argv names the home the server
// belongs to. The argv is readable where the environment is not: from another
// user namespace, which is where every other gate run on this box lives.
export interface TmuxProc {
  pid: number;
  args: string[];
}

function inside(p: string, dir: string): boolean {
  return p.startsWith(dir + path.sep);
}

// The homes a sweep of `root` removes: the abandoned directories in it, and
// the abandoned homes tmux servers still name after their directory is gone.
export function sweptHomes(
  root: string,
  names: string[],
  procs: TmuxProc[],
  alive: (pid: number) => boolean,
  mine?: number,
): Set<string> {
  const homes = new Set<string>();
  for (const n of names) if (abandoned(n, alive, mine)) homes.add(path.join(root, n));
  for (const p of procs) {
    for (const a of p.args) {
      if (!inside(a, root)) continue;
      const top = path.relative(root, a).split(path.sep)[0];
      if (abandoned(top, alive, mine)) homes.add(path.join(root, top));
    }
  }
  return homes;
}

// The tmux processes to kill when `homes` are removed: exactly those whose
// argv names a path inside one of them. A server naming no such path is never
// a run's, because the user's own node runs its shells on this box too.
export function tmuxToKill(procs: TmuxProc[], homes: ReadonlySet<string>): number[] {
  const hs = [...homes];
  return procs.filter((p) => p.args.some((a) => hs.some((h) => inside(a, h)))).map((p) => p.pid);
}
