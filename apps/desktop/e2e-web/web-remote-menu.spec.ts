import { test as base, expect, Page } from '@playwright/test';
import { execFileSync, spawn, ChildProcess } from 'node:child_process';
import * as net from 'node:net';
import * as os from 'node:os';
import * as path from 'node:path';
import * as fs from 'node:fs';
import { seedHome } from '../e2e/fixtures';
import { serveBin } from './fixtures';
import { GridwellDriver } from '../e2e/driver';
import { getGrid, tileAt } from '../e2e/oracle';

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
  world: { localOrigin: string; farOrigin: string; killFar: () => Promise<void> };
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

    // The connection is server.yaml config (v2 #269): declared before
    // first boot, reconciled into the transport at start — the picker
    // no longer exists to wire it "the data way".
    const localHome = seedHome(
      [{ kind: 'remote', name: 'rtb' }],
      `connections:
    - name: farconn1
      addr: 127.0.0.1:${farPort}
`,
    );
    const localPort = await freePort();
    const local = await spawnServe(localHome, localPort);

    const stop = (c: ChildProcess) =>
      new Promise<void>((res) => {
        if (c.exitCode !== null) {
          res();
          return;
        }
        const hard = setTimeout(() => {
          c.kill('SIGKILL');
          res();
        }, 3_000);
        c.once('exit', () => {
          clearTimeout(hard);
          res();
        });
        c.kill('SIGTERM');
      });

    await use({
      localOrigin: `http://127.0.0.1:${localPort}`,
      farOrigin: `http://127.0.0.1:${farPort}`,
      // The partition switch (stale-affordance spec): the far node dies
      // mid-session, exactly like a machine going dark.
      killFar: () => stop(far),
    });

    for (const c of [local, far]) {
      await stop(c);
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
  // ── The yaml-declared connection presents as its own menu row; its
  // root (the remote HOME) is learned through the direct dial and rides
  // the row's rootGridId (v2 #269 — the instance-row synthesis). ──
  let farHomeGrid = '';
  await expect
    .poll(
      async () => {
        const lp = await rpcJSON(world.localOrigin, 'ListPlugins', {});
        const row = (lp.plugins ?? []).find((p: any) => p.uuid?.endsWith('/farconn1'));
        farHomeGrid = row?.rootGridId ?? '';
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

  // The bar knows the DOOR (#263): the title is the link well's own name —
  // never the plugin's config label — and renaming here renames that well
  // (rename-where-you-stand). The level's crumb wears the mount's glyph
  // (the globe, ""), not a generic grid face (#264).
  await expect.poll(async () => (await gw.barName()).label).toBe('far');
  expect((await gw.barName()).editable, 'the door is a real row — renamable').toBe(true);
  const bar = await gw.bar();
  const rootCrumb = bar.segments.filter((s) => s.kind === 'chain' && s.anchor).pop();
  expect(rootCrumb?.glyph, 'the crumb wears the mount door face, not a grid').toBe('');
  await gw.clickBarName('right');
  const rin = window.locator('#gw-rename-input');
  await expect(rin).toBeVisible();
  await rin.fill('my far place');
  await rin.press('Enter');
  await expect
    .poll(async () => String(tileAt(await gw.getGrid(f.gridID), 'well', cx, cy)?.altText ?? ''))
    .toBe('my far place');
  await expect.poll(async () => (await gw.barName()).label).toBe('my far place');

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

// The stale affordance (#256): a mounted machine going dark degrades the
// remote pane to a cache-served MEMORY — the tiles render exactly as
// remembered ("stays as you left it") and the wire-level stale bit
// surfaces as the bar's quiet offline chip, read here via the panes()
// hook. Nothing moves, nothing blanks.
test('a dark mount serves the remembered room, marked stale', async ({ gw, window, world }) => {
  // The yaml-declared connection: its learned root IS the mount target.
  let farHomeGrid = '';
  await expect
    .poll(
      async () => {
        const lp = await rpcJSON(world.localOrigin, 'ListPlugins', {});
        const row = (lp.plugins ?? []).find((p: any) => p.uuid?.endsWith('/farconn1'));
        farHomeGrid = row?.rootGridId ?? '';
        return farHomeGrid;
      },
      { timeout: 20_000 },
    )
    .not.toBe('');
  const farLp = await rpcJSON(world.farOrigin, 'ListPlugins', {});
  await rpcJSON(world.farOrigin, 'CreateTile', {
    gridId: farLp.plugins[0].rootGridId,
    tile: { kind: 'text', x: 1, y: 1, w: 1, h: 1 },
  });

  // Link the far home into the local grid and descend: live first.
  await gw.enterPlugin('e2e');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await rpcJSON(world.localOrigin, 'CreateTile', {
    gridId: f.gridID,
    tile: { kind: 'well', x: cx, y: cy, w: 1, h: 1, childGridId: farHomeGrid, altText: 'far' },
  });
  await expect.poll(async () => !!tileAt(await gw.getGrid(f.gridID), 'well', cx, cy)).toBe(true);
  await gw.descendCell(cx, cy);
  let inside = (await gw.panes()).find((p) => p.focused)!;
  expect(inside.gridID).toBe(farHomeGrid);
  expect(inside.stale, 'a live remote room is not stale').toBeFalsy();
  const liveTiles = (await gw.getGrid(farHomeGrid)).tiles ?? [];
  expect(liveTiles.length).toBeGreaterThan(0);

  // The machine goes dark. Leave and re-enter: the room re-reads through
  // the mount cache and arrives as a marked memory, tiles intact.
  await world.killFar();
  await gw.ascendViaCrumb();
  await gw.descendCell(cx, cy);
  await expect
    .poll(async () => {
      const p = (await gw.panes()).find((q) => q.focused);
      return p?.gridID === farHomeGrid && p.stale === true;
    }, { message: 'the re-entered room says it is a memory (#256)', timeout: 20_000 })
    .toBe(true);
  const staleTiles = (await gw.getGrid(farHomeGrid)).tiles ?? [];
  expect(staleTiles.length, 'the memory renders every remembered tile').toBe(liveTiles.length);
  void window;
});
