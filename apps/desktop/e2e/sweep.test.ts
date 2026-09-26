import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { abandoned, sweptHomes, tmuxToKill } from './sweep';

const LIVE = 100;
const DEAD = 200;
const alive = (pid: number) => pid === LIVE;

test('abandoned: only a gridwell-e2e- directory whose owner is gone, or is the finished runner', () => {
  assert.equal(abandoned(`gridwell-e2e-p${DEAD}-abc`, alive), true);
  assert.equal(abandoned(`gridwell-e2e-p${LIVE}-abc`, alive), false, "another run's live home");
  assert.equal(abandoned(`gridwell-e2e-p${LIVE}-abc`, alive, LIVE), true, "the runner's own, at its teardown");
  assert.equal(abandoned('gridwell-e2e-abc', alive), true, 'no owner in the name');
  assert.equal(abandoned('gridwell-tmux-gridwell-k3x9m2q', alive), false);
  assert.equal(abandoned('gridwell', alive), false);
});

const conf = (home: string) => ['tmux', '-L', 'gridwell-k3x9m2q', '-f', `${home}/tmp/gridwell-tmux-gridwell-k3x9m2q/tmux.conf`, 'new-session'];

test('sweptHomes names an abandoned home a tmux server still points at after its directory is gone', () => {
  const homes = sweptHomes(
    '/tmp',
    [`gridwell-e2e-p${DEAD}-dir`, `gridwell-e2e-p${LIVE}-dir`, 'unrelated'],
    [
      { pid: 1, args: conf(`/tmp/gridwell-e2e-p${DEAD}-gone`) },
      { pid: 2, args: conf(`/tmp/gridwell-e2e-p${LIVE}-busy`) },
      { pid: 3, args: conf(`/elsewhere/gridwell-e2e-p${DEAD}-other`) },
      // The user's own node: its config dir sits straight in /tmp.
      { pid: 4, args: ['tmux', '-L', 'gridwell-u5er001', '-f', '/tmp/gridwell-tmux-gridwell-u5er001/tmux.conf'] },
      { pid: 5, args: ['tmux'] },
    ],
    alive,
  );
  assert.deepEqual([...homes].sort(), [`/tmp/gridwell-e2e-p${DEAD}-dir`, `/tmp/gridwell-e2e-p${DEAD}-gone`]);
});

test('tmuxToKill takes exactly the servers whose argv names a swept home', () => {
  const procs = [
    { pid: 1, args: conf('/tmp/gridwell-e2e-p200-a') },
    { pid: 2, args: ['tmux', '-S', '/tmp/gridwell-e2e-p200-a/tmp/tmux-1000/gridwell-k3x9m2q'] },
    { pid: 3, args: conf('/tmp/gridwell-e2e-p100-b') },
    // A sibling whose name only begins with a swept home's.
    { pid: 4, args: conf('/tmp/gridwell-e2e-p200-ab') },
    { pid: 5, args: ['tmux', '-L', 'gridwell-u5er001', '-f', '/tmp/gridwell-tmux-gridwell-u5er001/tmux.conf'] },
    { pid: 6, args: ['tmux'] },
  ];
  assert.deepEqual(tmuxToKill(procs, new Set(['/tmp/gridwell-e2e-p200-a'])), [1, 2]);
  assert.deepEqual(tmuxToKill(procs, new Set()), [], 'nothing swept, nothing killed');
});

// Drift lint across the language seam: tmuxToKill finds a run's servers only
// because the node puts tmux's config under os.TempDir() and passes it on
// every tmux argv.
test('the node names its temp dir in every tmux argv', () => {
  const src = fs.readFileSync(path.resolve(__dirname, '..', '..', '..', 'internal', 'local', 'tmux', 'tmux.go'), 'utf8');
  assert.ok(src.includes('filepath.Join(os.TempDir(), "gridwell-tmux-"+socketName)'), 'tmux.New no longer roots its dir in os.TempDir()');
  assert.ok(src.includes('args := []string{c.binary, "-L", c.socketName, "-f", c.configPath}'), 'Controller.Args no longer passes -f <config>');
});
