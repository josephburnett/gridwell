import { test as base, expect, Page } from '@playwright/test';
import * as os from 'node:os';
import * as path from 'node:path';
import * as fs from 'node:fs';
import { seedHome } from '../e2e/fixtures';
import { Served, spawnServe, stopServe, freePort, authHeaders, authenticate } from './fixtures';
import { GridwellDriver } from '../e2e/driver';
import { getGrid, tileAt } from '../e2e/oracle';

// Descending into a node means being there. Two real nodes over a direct
// connection, with no sshd anywhere: descending the connection lands on the
// remote's home, the + menu inside that pane shows the remote node's plugins,
// exactly what a direct client of it sees, a primitive dragged from that menu
// creates on the remote node, and dropping it into a local pane refuses
// visibly, because a menu belongs to its node.

const SERVICE = 'gridwell.v1.Gridwell';

async function rpcJSON(node: Served, method: string, body: unknown): Promise<any> {
  const res = await fetch(`${node.origin}/${SERVICE}/${method}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'Connect-Protocol-Version': '1', ...authHeaders(node) },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`${method} failed: ${res.status} ${await res.text()}`);
  return res.json();
}

type Fixtures = {
  world: { local: Served; far: Served; killFar: () => Promise<void>; reviveFar: () => Promise<void> };
  window: Page;
  gw: GridwellDriver;
};

const test = base.extend<Fixtures>({
  // world is the local node and the far node, directly connected. The far
  // node's fresh home gets its id from its first serve.
  world: async ({}, use) => {
    const farHome = fs.mkdtempSync(path.join(os.tmpdir(), 'gridwell-far-'));
    fs.writeFileSync(path.join(farHome, 'server.yaml'), '');
    const farPort = await freePort();
    let far = await spawnServe(farHome, farPort);

    // The connection is server.yaml config: declared before the first boot and
    // reconciled into the transport at start.
    const localHome = seedHome(
      [],
      `connections:
    - name: farconn1
      addr: ${path.join(farHome, 'federation.sock')}
`,
    );
    const localPort = await freePort();
    const local = await spawnServe(localHome, localPort);

    await use({
      local,
      // A getter, because reviveFar replaces the process: a spec that killed
      // the machine and brought it back must reach the one running now.
      get far() {
        return far;
      },
      // The far node dies mid-session, like a machine going dark.
      killFar: () => stopServe(far.child),
      // The same machine coming back: same home, same address, same node id, so
      // the connection self-heals rather than landing somewhere new. This is
      // test/connections/partition_test.go's revival shape in the browser
      // gate.
      reviveFar: async () => {
        far = await spawnServe(farHome, farPort);
      },
    });

    for (const c of [local, far]) {
      await stopServe(c.child);
    }
    fs.rmSync(localHome, { recursive: true, force: true });
    fs.rmSync(farHome, { recursive: true, force: true });
  },
  window: async ({ world, page }, use) => {
    await authenticate(page, world.local);
    await page.goto(world.local.origin + '/?e2e=1');
    await page.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
    await use(page);
  },
  gw: async ({ window, world }, use) => {
    await use(new GridwellDriver(window, world.local.origin));
  },
});

// enterFarRoom is the shared preamble of the mount specs below: learn the
// yaml-declared connection's root, put one tile in the far node's home, link
// that home into the local grid, descend into it, and wait for the room to
// arrive live. It returns the far room's qualified grid id and the cell the
// link well sits on.
//
// The node serves a remembered grid immediately and revalidates behind it, and
// the client's own Subscribe prefetched this connection at boot, before the far
// node grew the tile, so the first answers for this room are a legitimately
// empty memory inside its freshness window. A single read races the correction
// the revalidation's GridChanged brings, hence the poll.
async function enterFarRoom(
  gw: GridwellDriver,
  world: { local: Served; far: Served },
): Promise<{ farHomeGrid: string; cx: number; cy: number; localGrid: string; liveTiles: number }> {
  let farHomeGrid = '';
  await expect
    .poll(
      async () => {
        const lp = await rpcJSON(world.local, 'Handshake', {});
        const row = (lp.connections ?? []).find((p: any) => p.uuid?.endsWith('/farconn1'));
        farHomeGrid = row?.rootGridId ?? '';
        return farHomeGrid;
      },
      { timeout: 20_000 },
    )
    .not.toBe('');
  const farLp = await rpcJSON(world.far, 'Handshake', {});
  await rpcJSON(world.far, 'CreateTile', {
    gridId: farLp.plugins[0].rootGridId,
    tile: { kind: 'text', x: 1, y: 1, w: 1, h: 1 },
  });

  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await rpcJSON(world.local, 'CreateTile', {
    gridId: f.gridID,
    tile: { kind: 'well', x: cx, y: cy, w: 1, h: 1, childGridId: farHomeGrid, altText: 'far' },
  });
  await expect.poll(async () => !!tileAt(await gw.getGrid(f.gridID), 'well', cx, cy)).toBe(true);
  await gw.descendCell(cx, cy);
  const inside = (await gw.panes()).find((p) => p.focused)!;
  expect(inside.gridID).toBe(farHomeGrid);
  expect(inside.stale, 'a live remote room is not a memory').toBeFalsy();
  let liveTiles = 0;
  await expect
    .poll(
      async () => {
        liveTiles = ((await gw.getGrid(farHomeGrid)).tiles ?? []).length;
        return liveTiles;
      },
      { message: 'the live room arrives, remembered-empty first or not', timeout: 20_000 },
    )
    .toBeGreaterThan(0);
  return { farHomeGrid, cx, cy, localGrid: f.gridID, liveTiles };
}

test('the + menu inside a remote pane is the remote node, and its creations land there', async ({
  gw,
  window,
  world,
}) => {
  // ── The yaml-declared connection presents as its own menu row. Its root, the
  // remote home, is learned through the direct dial and rides the row's
  // rootGridId. ──
  let farHomeGrid = '';
  await expect
    .poll(
      async () => {
        const lp = await rpcJSON(world.local, 'Handshake', {});
        const row = (lp.connections ?? []).find((p: any) => p.uuid?.endsWith('/farconn1'));
        farHomeGrid = row?.rootGridId ?? '';
        return farHomeGrid;
      },
      { timeout: 20_000 },
    )
    .not.toBe('');
  const farLp = await rpcJSON(world.far, 'Handshake', {});
  expect(farHomeGrid.endsWith('/' + farLp.plugins[0].rootGridId), 'the landing is the remote HOME').toBe(
    true,
  );

  // ── A link well to the far home, dropped where the client looks ──
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await rpcJSON(world.local, 'CreateTile', {
    gridId: f.gridID,
    tile: { kind: 'well', x: cx, y: cy, w: 1, h: 1, childGridId: farHomeGrid, altText: 'far' },
  });
  await expect
    .poll(async () => !!tileAt(await gw.getGrid(f.gridID), 'well', cx, cy))
    .toBe(true);

  // ── Descend: the pane is there, and the menu is the far node's. ──
  await gw.descendCell(cx, cy);

  // The bar's title is the link well's own name, never the remote's config
  // label, and renaming here renames that well. The level's crumb wears the
  // mount's glyph, the globe, rather than a generic grid face.
  await expect.poll(async () => (await gw.barName()).label).toBe('far');
  expect((await gw.barName()).editable, 'the door is a real row — renamable').toBe(true);
  const bar = await gw.bar();
  const rootCrumb = bar.segments.filter((s) => s.kind === 'chain' && s.anchor).pop();
  expect(rootCrumb?.glyph, 'the crumb wears the mount door face, not a grid').toBe('globe');
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
  await gw.expandPlugins();
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
    // The far node's own menu, declared entries included: its home brings its
    // own trashcan, so a delete over there files over there.
    .toBe('home,home · trash');

  // ── A primitive from the remote menu creates on the remote node ──
  const inside = await gw.focused();
  const icx = Math.round(inside.cx);
  const icy = Math.round(inside.cy);
  await gw.dragCreate('markdown', icx, icy);
  const farRootBare = farLp.plugins[0].rootGridId;
  const farSnap = await getGrid(world.far.origin, farRootBare);
  expect(tileAt(farSnap, 'text', icx, icy), 'the text tile exists ON THE FAR NODE').toBeTruthy();

  // ── The refusal: a remote menu's primitive dropped into a local pane ──
  await gw.splitFocusedPaneVertical();
  // After a split the new sibling shows the same place, so ascend it back to the
  // local grid before dragging into it.
  const panes = await window.evaluate(() => (window as any).__gridwellTest.panes());
  const other = panes.find((p: any) => p.id !== inside.id);
  expect(other, 'the split produced a sibling').toBeTruthy();
  await gw.clickScreen(other.x + other.w / 2, other.y + 10);
  await window.mouse.click(other.x + other.w / 2, other.y + other.h / 2, { button: 'middle' });
  await gw.waitIdle();
  const sib = await gw.focused();
  expect(sib.gridID, 'the sibling ascended to the local grid').toBe(f.gridID);

  // Back to the remote pane: drag markdown from its menu into the local
  // sibling. The refusal is visible and no tile appears.
  await gw.clickScreen(inside.x + 20, inside.y + 20);
  await gw.openPalette();
  const pal = await window.evaluate(() => (window as any).__gridwellTest.palette());
  const md = pal.items.find((i: any) => !i.isPlugin && i.kind === 'markdown');
  expect(md, 'the remote menu offers markdown').toBeTruthy();
  const target = await gw.cellCenter(sib.id, Math.round(sib.cx) + 1, Math.round(sib.cy) + 1);
  const before = ((await gw.getGrid(f.gridID)).tiles ?? []).length;
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
  expect(((await gw.getGrid(f.gridID)).tiles ?? []).length, 'no cross-node tile was created').toBe(before);
});

// A mounted machine going dark degrades the remote pane to a cache-served
// memory: the tiles render exactly as remembered, and the bar shows its quiet
// offline chip, read here through the panes() hook. Nothing moves and nothing
// blanks.
test('a dark mount serves the remembered room, marked stale', async ({ gw, window, world }) => {
  const { farHomeGrid, cx, cy, liveTiles } = await enterFarRoom(gw, world);

  // Leave and re-enter: the room re-reads through the source cache and arrives
  // whole. What says it is a memory is the connection's health, which the node
  // publishes when the far node's stream ends and the client folds in per
  // source. Hence the poll: the chip lands a beat after the machine does.
  await world.killFar();
  await gw.ascendViaCrumb();
  await gw.descendCell(cx, cy);
  await expect
    .poll(async () => {
      const p = (await gw.panes()).find((q) => q.focused);
      return p?.gridID === farHomeGrid && p.stale === true;
    }, { message: 'the re-entered room says it is a memory (#256)', timeout: 60_000 })
    .toBe(true);
  const staleTiles = (await gw.getGrid(farHomeGrid)).tiles ?? [];
  expect(staleTiles.length, 'the memory renders every remembered tile').toBe(liveTiles);
  void window;
});

// The machine comes back and everything its going dark caused undoes itself
// with nobody touching anything. This is the client half of the two rows
// docs/freshness.md's gap list left open:
//
//   (a) the GridChanged arm clears the per-grid failure latch and refetches.
//   (b) reportPluginHealth folds the transition into the cache and kicks a
//       resync scoped to the source the event names, in BOTH directions, and
//       its sticky notice resolves on recovery. This room is served through the
//       connection that flapped, so it is inside that scope (cache.ServedBy),
//       and the chip is that same fact drawn (cache.SourceDark).
//
// Both are only observable as the absence of a gesture, so after the far node
// dies, and again after it revives, this spec polls the client's own state and
// does nothing else. Every refetch it sees was the client's own reaction to an
// event.
test('a revived mount clears its chip and its notice with nobody touching anything', async ({
  gw,
  window,
  world,
}) => {
  test.setTimeout(240_000);
  const { farHomeGrid } = await enterFarRoom(gw, world);
  const notices = async (): Promise<{ source: string; message: string }[]> =>
    (await window.evaluate(() => (window as any).__gridwellTest.errors())).notices ?? [];
  const health = async () => (await notices()).filter((n) => String(n.source).startsWith('plugin:'));
  const focusedStale = async () => {
    const p = (await gw.panes()).find((q) => q.focused);
    return p?.gridID === farHomeGrid ? p.stale === true : null;
  };
  expect(await health(), 'a live mount posts no health notice').toEqual([]);

  // ── Down ──────────────────────────────────────────────────────────────
  // The connection's stream ends, the node publishes one health event, and the
  // client posts the sticky notice keyed by the connection's uuid.
  await world.killFar();
  await expect
    .poll(async () => (await health()).map((n) => n.message).join('|'), {
      message: 'the down transition reaches the strip',
      timeout: 60_000,
    })
    .toContain('live updates stopped');
  // A source going down changes what its grids ARE: the room is the node's
  // memory of it from here on, and the chip says so with no gesture.
  await expect
    .poll(focusedStale, {
      message: 'the chip appears with no gesture',
      timeout: 60_000,
    })
    .toBe(true);

  // ── Up ────────────────────────────────────────────────────────────────
  // The healthy event resolves the notice, clears the darkness, and kicks the
  // same resync; the revalidation's GridChanged clears the latch behind it.
  await world.reviveFar();
  await expect
    .poll(async () => (await health()).length, {
      message: 'the recovery resolves the notice',
      timeout: 120_000,
    })
    .toBe(0);
  await expect
    .poll(focusedStale, {
      message: 'the chip clears with no gesture (freshness.md trace (b))',
      timeout: 120_000,
    })
    .toBe(false);
});

// The remote menu is a deduped read like any other, and the class here is a
// claim that outlives the fetch it guards. The client asks the far node for its
// menu once per node namespace and holds a claim on that namespace while the
// read is out; a request the network swallows, never fulfilled and never
// aborted, would hold it for the life of the page, leaving the remote pane's +
// menu with no plugin section at all and nothing on the error strip.
//
// The read is bounded, so the menu fills itself in off its own clock with the
// link in exactly the state that broke it. Nothing here kills anything and
// nothing restarts. A health flap on the node the claim names also cancels it,
// which is faster when one happens; client/inflight and client/cache's unit
// tests own that half.
test('a menu read the network swallows does not latch the remote menu empty', async ({
  gw,
  window,
  world,
}) => {
  test.setTimeout(150_000);
  await enterFarRoom(gw, world);

  // The next Handshake that names the connection is swallowed. The boot
  // handshake (no namespace) and the retry both keep a live link, so the only
  // thing between the pane and the far node's menu is the client's own claim on
  // that namespace.
  let blackhole = true;
  await window.route(`**/${SERVICE}/Handshake`, async (route) => {
    if (blackhole && (route.request().postData() ?? '').includes('farconn1')) {
      blackhole = false; // one read into the hole; the retry gets a live link
      return;
    }
    await route.continue();
  });

  await gw.openPalette();
  const pal = await window.evaluate(() => (window as any).__gridwellTest.palette());
  expect(pal.open, 'the remote pane has its menu open').toBe(true);
  expect(pal.toggle.present, 'the swallowed read leaves no plugin section to unfold').toBe(false);

  // No user action beyond keeping the menu open: the bounded read gives up,
  // says so on the strip, and the next draw of the menu asks again over a link
  // that works. Unfolding the section is part of reading it, and it cannot be
  // unfolded until there is something to unfold.
  let sawNotice = false;
  await expect
    .poll(
      async () => {
        const errs = await window.evaluate(() => (window as any).__gridwellTest.errors());
        if (errs.notices.some((n: any) => n.source === 'rpc:Handshake')) sawNotice = true;
        const p = await window.evaluate(() => (window as any).__gridwellTest.palette());
        if (p.toggle?.present && !p.toggle.expanded) {
          await window.mouse.click(p.toggle.x + p.toggle.w / 2, p.toggle.y + p.toggle.h / 2);
        }
        const now = await window.evaluate(() => (window as any).__gridwellTest.palette());
        return (now.items ?? [])
          .filter((i: any) => i.isPlugin)
          .map((i: any) => i.label)
          .join(',');
      },
      { message: 'the far node’s menu arrives by itself', timeout: 75_000 },
    )
    // The same roster the spec above pins, asked for here after a swallowed
    // read rather than on the first try.
    .toBe('home,home · trash');
  expect(sawNotice, 'the swallowed read surfaced rather than disappearing').toBe(true);
});
