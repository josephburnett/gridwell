import { test as base, expect, Page } from '@playwright/test';
import { execFileSync, spawn, ChildProcess } from 'node:child_process';
import * as net from 'node:net';
import * as os from 'node:os';
import * as path from 'node:path';
import * as fs from 'node:fs';
import { seedHome } from '../e2e/fixtures';
import { serveBin } from './fixtures';
import { GridwellDriver } from '../e2e/driver';
import { getGrid, writeContent, tileAt } from '../e2e/oracle';

// The REMOTE MENU seam (docs/remote-menu.md, 2026-08-16: "when I descend
// into a node, I am there"): two real nodes over a DIRECT connection —
// no sshd anywhere. Descending the connection lands on the remote's
// HOME, the + menu inside that pane shows the REMOTE node's plugins
// (exactly what a direct client of it sees), a primitive dragged from
// that menu creates on the REMOTE node, and dropping it into a LOCAL
// pane refuses VISIBLY — a menu belongs to its node.

const REPO_ROOT = path.resolve(__dirname, '..', '..', '..');
const SERVICE = 'gridwell.v1.Gridwell';

async function rpcJSON(origin: string, method: string, body: unknown): Promise<any> {
  const res = await fetch(`${origin}/${SERVICE}/${method}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Connect-Protocol-Version': '1' },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`${method} failed: ${res.status} ${await res.text()}`);
  return res.json();
}

function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = net.createServer();
    srv.on('error', reject);
    srv.listen(0, '127.0.0.1', () => {
      const port = (srv.address() as net.AddressInfo).port;
      srv.close(() => resolve(port));
    });
  });
}

// spawnServe boots one gridwell node for a home and polls readiness.
async function spawnServe(home: string, port: number): Promise<ChildProcess> {
  const child = spawn(
    serveBin(),
    ['serve', '--bind', `127.0.0.1:${port}`, '--static', path.join(REPO_ROOT, 'web')],
    { env: { ...process.env, GRIDWELL_HOME: home }, stdio: ['ignore', 'pipe', 'pipe'] },
  );
  const origin = `http://127.0.0.1:${port}`;
  const deadline = Date.now() + 15_000;
  for (;;) {
    try {
      if ((await fetch(origin + '/')).ok) break;
    } catch {
      // not up yet
    }
    if (Date.now() > deadline) {
      child.kill('SIGKILL');
      throw new Error(`serve not ready on ${origin}`);
    }
    await new Promise((r) => setTimeout(r, 100));
  }
  return child;
}

type Fixtures = {
  world: { localOrigin: string; farOrigin: string };
  window: Page;
  gw: GridwellDriver;
};

const test = base.extend<Fixtures>({
  // world = the LOCAL node (e2e local + rtb remote plugin) and the FAR
  // node (one local plugin named farlocal), directly connected.
  world: async ({}, use) => {
    const farHome = fs.mkdtempSync(path.join(os.tmpdir(), 'gridwell-far-'));
    execFileSync(serveBin(), ['init', '--kind', 'local', '--name', 'farlocal'], {
      env: { ...process.env, GRIDWELL_HOME: farHome },
      stdio: 'ignore',
    });
    const farPort = await freePort();
    const far = await spawnServe(farHome, farPort);

    const localHome = seedHome([{ kind: 'remote', name: 'rtb' }]);
    const localPort = await freePort();
    const local = await spawnServe(localHome, localPort);

    await use({
      localOrigin: `http://127.0.0.1:${localPort}`,
      farOrigin: `http://127.0.0.1:${farPort}`,
    });

    for (const c of [local, far]) {
      c.kill('SIGTERM');
      await new Promise<void>((res) => {
        const hard = setTimeout(() => {
          c.kill('SIGKILL');
          res();
        }, 3_000);
        c.once('exit', () => {
          clearTimeout(hard);
          res();
        });
      });
    }
    fs.rmSync(localHome, { recursive: true, force: true });
    fs.rmSync(farHome, { recursive: true, force: true });
  },
  window: async ({ world, page }, use) => {
    await page.goto(world.localOrigin + '/?e2e=1');
    await page.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
    await use(page);
  },
  gw: async ({ window, world }, use) => {
    await use(new GridwellDriver(window, world.localOrigin));
  },
});

test('the + menu inside a remote pane is the remote node, and its creations land there', async ({
  gw,
  window,
  world,
}) => {
  // ── Wire the direct connection (the data way: well + params commit) ──
  const lp = await rpcJSON(world.localOrigin, 'ListPlugins', {});
  const rtb = lp.plugins.find((p: any) => p.kind === 'remote');
  const conn = (
    await rpcJSON(world.localOrigin, 'CreateTile', {
      gridId: rtb.instanceGridId,
      tile: { kind: 'well', x: 0, y: 0, w: 1, h: 1 },
    })
  ).tile;
  const farAddr = world.farOrigin.replace('http://', '');
  await writeContent(
    world.localOrigin,
    conn.id,
    Number(conn.version ?? 0),
    Buffer.from(JSON.stringify({ addr: farAddr })),
  );
  // The connection's child = the remote HOME (farlocal's root), learned
  // through the direct dial.
  let farHomeGrid = '';
  await expect
    .poll(
      async () => {
        const g = await rpcJSON(world.localOrigin, 'GetGrid', { gridId: rtb.instanceGridId });
        farHomeGrid = (g.tiles ?? []).map((t: any) => t.childGridId).find(Boolean) ?? '';
        return farHomeGrid;
      },
      { timeout: 20_000 },
    )
    .not.toBe('');
  const farLp = await rpcJSON(world.farOrigin, 'ListPlugins', {});
  expect(farHomeGrid.endsWith('/' + farLp.plugins[0].rootGridId), 'the landing is the remote HOME').toBe(
    true,
  );

  // ── A link well to the far home, dropped where the client looks ──
  await gw.enterPlugin('e2e');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await rpcJSON(world.localOrigin, 'CreateTile', {
    gridId: f.gridID,
    tile: { kind: 'well', x: cx, y: cy, w: 1, h: 1, childGridId: farHomeGrid, altText: 'far' },
  });
  await expect
    .poll(async () => !!tileAt(await gw.getGrid(f.gridID), 'well', cx, cy))
    .toBe(true);

  // ── Descend: I AM THERE. The menu is the far node's. ──
  await gw.descendCell(cx, cy);
  await gw.openPalette();
  await expect
    .poll(
      async () => {
        const pal = await window.evaluate(() => (window as any).__gridwellTest.palette());
        return (pal.items ?? [])
          .filter((i: any) => i.isPlugin)
          .map((i: any) => i.label)
          .join(',');
      },
      { timeout: 15_000 },
    )
    // The far node's own menu, root entries included: its local plugin
    // brings its own trashcan (#262) — deletes over there file over there.
    .toBe('farlocal,trash');

  // ── A primitive from the remote menu creates on the REMOTE node ──
  const inside = await gw.focused();
  const icx = Math.round(inside.cx);
  const icy = Math.round(inside.cy);
  await gw.dragCreate('markdown', icx, icy);
  const farRootBare = farLp.plugins[0].rootGridId;
  const farSnap = await getGrid(world.farOrigin, farRootBare);
  expect(tileAt(farSnap, 'text', icx, icy), 'the text tile exists ON THE FAR NODE').toBeTruthy();

  // ── The refusal: a remote menu's primitive dropped into a LOCAL pane ──
  await gw.splitFocusedPaneVertical();
  // After a split the new sibling shows the same place; refocus pane A
  // (the remote pane) — the split leaves focus there; ascend the SIBLING
  // to the local grid: click it, ascend via middle-click (one step =
  // portal back to e2e). Simpler: pane B is a clone of the remote pane;
  // drag from A's menu into B after B ascends home.
  const panes = await window.evaluate(() => (window as any).__gridwellTest.panes());
  const other = panes.find((p: any) => p.id !== inside.id);
  expect(other, 'the split produced a sibling').toBeTruthy();
  // Focus the sibling and ascend it out of the portal to the LOCAL grid.
  await gw.clickScreen(other.x + other.w / 2, other.y + 10);
  await window.mouse.click(other.x + other.w / 2, other.y + other.h / 2, { button: 'middle' });
  await gw.waitIdle();
  const sib = await gw.focused();
  expect(sib.gridID, 'the sibling ascended to the local grid').toBe(f.gridID);

  // Back to the REMOTE pane; open ITS menu and drag markdown into the
  // sibling (local) pane: visible refusal, no tile.
  await gw.clickScreen(inside.x + 20, inside.y + 20);
  await gw.openPalette();
  const pal = await window.evaluate(() => (window as any).__gridwellTest.palette());
  const md = pal.items.find((i: any) => !i.isPlugin && i.kind === 'markdown');
  expect(md, 'the remote menu offers markdown').toBeTruthy();
  const target = await gw.cellCenter(sib.id, Math.round(sib.cx) + 1, Math.round(sib.cy) + 1);
  const before = (await gw.getGrid(f.gridID)).tiles.length;
  await window.mouse.move(md.x + md.w / 2, md.y + md.h / 2);
  await window.mouse.down();
  await window.mouse.move(md.x + md.w / 2 + 10, md.y + md.h / 2 + 10);
  await window.mouse.move(target.x, target.y, { steps: 5 });
  await window.mouse.up();
  await gw.waitIdle();
  await expect
    .poll(async () => {
      const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
      return errs.notices.some((n: any) => n.source === 'menu');
    })
    .toBe(true);
  expect((await gw.getGrid(f.gridID)).tiles.length, 'no cross-node tile was created').toBe(before);
});
