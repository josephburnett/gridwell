import { test, expect } from './fixtures';
import * as fs from 'node:fs';
import * as http from 'node:http';
import * as net from 'node:net';
import * as os from 'node:os';
import * as path from 'node:path';

// Every shipped plugin kind, crawled through the browser client: proc, a live
// process tree, and gitlab, todos against a fake GitLab API. Each runs as a
// spawned gridwell-plugin-<kind> subprocess, which is the one way a plugin
// loads; seeding only fs would leave proc and gitlab uncrossed.

// A todo the fake serves. The JSON shape is the wire form of
// plugins/gitlab/todos.Todo, from GET /api/v4/todos: one pending review request.
const TODO = {
  id: 7,
  action_name: 'review_requested',
  target_type: 'MergeRequest',
  target_url: 'https://gitlab.example/g/p/-/merge_requests/7',
  body: 'please **review**',
  state: 'pending',
  created_at: '2026-08-24T10:00:00Z',
  updated_at: '2026-08-24T10:00:00Z',
  project: { id: 1, name: 'p', path_with_namespace: 'g/p', web_url: 'https://gitlab.example/g/p' },
  author: { name: 'Ada', username: 'ada', web_url: 'https://gitlab.example/ada' },
  target: { iid: 7, title: 'change the thing', state: 'opened' },
};
const TOKEN = 'glpat-e2e-fake';

// fakeGitLab answers the two endpoints the plugin speaks — the todos pager and
// mark_as_done — and refuses a wrong token with a 401 the way GitLab does, so
// a seeded token_file that did not reach the plugin is a visible failure
// rather than an empty grid. Marking done flips the one todo's state, the way
// the real API does, so a later walk agrees with the write.
function fakeGitLab(): Promise<http.Server> {
  TODO.state = 'pending'; // each server starts undone, whatever an earlier test wrote
  const srv = http.createServer((req, res) => {
    const url = new URL(req.url ?? '/', 'http://x');
    if (req.headers['private-token'] !== TOKEN) {
      res.writeHead(401, { 'content-type': 'application/json' }).end('{"message":"401 Unauthorized"}');
      return;
    }
    const done = /^\/api\/v4\/todos\/(\d+)\/mark_as_done$/.exec(url.pathname);
    if (req.method === 'POST' && done) {
      if (Number(done[1]) !== TODO.id) {
        res.writeHead(404).end();
        return;
      }
      TODO.state = 'done';
      res.writeHead(201, { 'content-type': 'application/json' }).end(JSON.stringify(TODO));
      return;
    }
    if (url.pathname !== '/api/v4/todos') {
      res.writeHead(404).end();
      return;
    }
    const page = Number(url.searchParams.get('page') ?? '1');
    const body = url.searchParams.get('state') === TODO.state && page === 1 ? [TODO] : [];
    res.writeHead(200, { 'content-type': 'application/json' }).end(JSON.stringify(body));
  });
  return new Promise((resolve) => srv.listen(0, '127.0.0.1', () => resolve(srv)));
}

test.use({
  // The seeded plugins need the fake API's port, so the option is a fixture here:
  // stand the fake up, then seed both plugins against it.
  extraPlugins: async ({}, use) => {
    const api = await fakeGitLab();
    const port = (api.address() as net.AddressInfo).port;
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'gridwell-web-plugins-'));
    const tokenFile = path.join(dir, 'token');
    fs.writeFileSync(tokenFile, TOKEN + '\n', { mode: 0o600 });
    await use([
      // proc rooted at this worker process: the served node is its child.
      { kind: 'proc', name: 'procs', config: { pid: String(process.pid) } },
      { kind: 'gitlab', name: 'todos', config: { url: `http://127.0.0.1:${port}`, token_file: tokenFile } },
    ]);
    await new Promise<void>((r) => api.close(() => r()));
    fs.rmSync(dir, { recursive: true, force: true });
  },
});

test('proc: the root grid lists the served node as a child of this worker', async ({ gw, serve }) => {
  await gw.enterPlugin('procs');
  const f = await gw.focused();
  const snap = await gw.getGrid(f.gridID);
  const tiles = snap.tiles ?? [];
  const info = tiles.find((t) => t.altText === '@info');
  expect(info, 'the process metadata tile is listed').toBeTruthy();
  const child = tiles.find((t) => t.altText === String(serve.child.pid));
  expect(child, `pid ${serve.child.pid} (gridwell serve) is a child well of ${process.pid}`).toBeTruthy();
  expect(child!.kind).toBe('well');

  // Descend into the served node's process: its own @info tile is there, so the
  // child context round-trips through the node's id space.
  await gw.descendCell(Number(child!.x ?? 0), Number(child!.y ?? 0));
  const inner = await gw.focused();
  expect(inner.gridID).not.toBe(f.gridID);
  const innerSnap = await gw.getGrid(inner.gridID);
  expect((innerSnap.tiles ?? []).some((t) => t.altText === '@info'), 'the child process has its @info').toBe(true);
});

test('gitlab: the week well descends to the todo, whose content is its markdown', async ({ gw }) => {
  await gw.enterPlugin('todos');
  const f = await gw.focused();
  const snap = await gw.getGrid(f.gridID);
  const week = (snap.tiles ?? []).find((t) => /^\d{4}-\d{2}-\d{2} · 1 open · 0 done$/.test(String(t.altText)));
  expect(week, `one week well with the one open todo; have ${JSON.stringify((snap.tiles ?? []).map((t) => t.altText))}`).toBeTruthy();

  await gw.descendCell(Number(week!.x ?? 0), Number(week!.y ?? 0));
  const inner = await gw.focused();
  const todos = (await gw.getGrid(inner.gridID)).tiles ?? [];
  const todo = todos.find((t) => String(t.altText).startsWith('Ada: !7'));
  expect(todo, `the todo tile is labeled by author and ref; have ${JSON.stringify(todos.map((t) => t.altText))}`).toBeTruthy();

  // ReadContent is the todo's markdown, served through the node exactly like any
  // text tile: the oracle is the RPC, not the plugin.
  expect(await gw.getTileContent(todo!.id)).toContain('please **review**');

  // The trash gesture on a todo means mark-as-done: the tile does not vanish,
  // it re-lists resolved — GitLab took the write (the fake flips its state),
  // the label wears the ✓ and the week's counts move. The whole journey rides
  // the one delete path: drag → DeleteTile → plugin Delete → mark_as_done.
  await gw.deleteTileCell(Number(todo!.x ?? 0), Number(todo!.y ?? 0));
  const after = (await gw.getGrid(inner.gridID)).tiles ?? [];
  const doneTile = after.find((t) => t.id === todo!.id);
  expect(doneTile, 'the todo is still listed after the gesture').toBeTruthy();
  expect(doneTile!.statusDetail).toBe('done');
  expect(String(doneTile!.altText)).toContain('✅');
});
