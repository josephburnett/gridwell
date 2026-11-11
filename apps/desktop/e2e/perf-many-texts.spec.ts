import { test, expect, homePassword, loginToken } from './fixtures';
import { createDoor, createText } from './oracle';
import { dumpNow, type TraceLine } from './trace';

// A grid of many long text tiles zooms and pans at frame rate: the paint
// budget a trackpad's bursts of wheel events and a pan drag are held to,
// read from the client's own draw records (traceevent.DrawAsks.Drawn). A
// burst is several wheel events in one task, as a trackpad sends them faster
// than the display paints.

// P95_BUDGET_MS is the paint the spec allows at the 95th percentile.
const P95_BUDGET_MS = 16;
const TILES_W = 15;
const TILES_H = 8;
const BURSTS = 40;
const PER_BURST = 4;

// body is a note of many short lines, 8 to 30 KB, different per tile.
function body(i: number): string {
  const target = 8_000 + ((i * 7919) % 22_000);
  const lines: string[] = [`# note ${i}`];
  let n = 0;
  for (let k = 0; n < target; k++) {
    const ln = `- item ${i}.${k} ${'lorem ipsum dolor '.slice(0, 6 + ((i + k) % 12))}${'x'.repeat((k * 13) % 30)}`;
    lines.push(ln);
    n += ln.length + 1;
  }
  return lines.join('\n');
}

function pct(xs: number[], p: number): number {
  const s = [...xs].sort((a, b) => a - b);
  return s[Math.min(s.length - 1, Math.floor((s.length - 1) * p))];
}

test('a grid of many long texts zooms and pans within the paint budget', async ({ gw, home, window, electronApp }, testInfo) => {
  test.setTimeout(240_000);
  // The block is 960 by 512 px at the grid's first zoom, so it all shows.
  await gw.resizeWindow(electronApp, 1200, 720);
  await gw.enterSource('home');
  const f0 = await gw.focused();
  const cx = Math.round(f0.cx);
  const cy = Math.round(f0.cy);
  const door = await createDoor(gw.origin, f0.gridID, cx, cy);
  const grid = door.childGridId!;
  expect(grid, 'the door has a grid').toBeTruthy();
  const ids = new Set<string>();
  for (let j = 0; j < TILES_H; j++) {
    for (let i = 0; i < TILES_W; i++) {
      const t = await createText(gw.origin, grid, i - Math.floor(TILES_W / 2), j - TILES_H / 2, body(j * TILES_W + i));
      ids.add(t.id);
    }
  }
  await expect.poll(async () => (await gw.getGrid(f0.gridID)).tiles?.some((t) => t.id === door.id)).toBe(true);
  await gw.waitIdle();

  const entered = Date.now();
  await gw.descendCell(cx, cy);
  expect((await gw.focused()).gridID, 'descended into the door').toBe(grid);
  // A grid's first visit fits what it holds; one cell is then 64 px.
  for (let i = 0; i < 40 && (await gw.focused()).zoom > 1; i++) await gw.wheelAtFocusedCenter(60);
  const f = await gw.focused();
  // Every body painted: the paint under test is the steady one, not the load.
  await expect
    .poll(
      async () => {
        const painted: Record<string, string> = await window.evaluate(() => (window as any).__gridwellTest.paintedText());
        return [...ids].filter((id) => (painted[id] ?? '').length > 0).length;
      },
      { timeout: 60_000 },
    )
    .toBe(ids.size);
  await gw.waitIdle();

  const token = await loginToken(gw.origin, homePassword(home));
  const before = await dumpNow(window, gw.origin, token);
  const mark = Math.max(0, ...before.lines.map((r) => r.seq ?? 0));
  const loaded = before.lines.filter(
    (r) => r.origin === 'client' && r.src === 'rpc' && r.msg === 'ReadContent ok' && ids.has(r.kv?.id ?? ''),
  );
  const firstBodiesMs = Math.max(...loaded.map((r) => r.ct ?? 0)) - entered;

  // Wheel bursts at the pane center, in then out.
  const wheels = async (dy: number, n: number) => {
    for (let b = 0; b < n; b++) {
      await window.evaluate(
        ([x, y, d, k]) => {
          const c = document.querySelector('canvas')!;
          for (let e = 0; e < k; e++) {
            c.dispatchEvent(new WheelEvent('wheel', { clientX: x, clientY: y, deltaY: d, bubbles: true, cancelable: true }));
          }
        },
        [f.x + f.w / 2, f.y + f.h / 2, dy, PER_BURST] as [number, number, number, number],
      );
      await window.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
    }
  };
  const wheelStart = Date.now();
  await wheels(-8, BURSTS / 2);
  await wheels(8, BURSTS / 2);
  const wheelEnd = Date.now();
  await gw.waitIdle();

  // A pan drag from the empty column left of the block.
  const m = window.mouse;
  const px = f.x + 16;
  const py = f.y + f.h / 2;
  await m.move(px, py);
  await m.down();
  await m.move(px + 300, py + 40, { steps: 60 });
  await m.up();
  await gw.waitIdle();
  const panEnd = Date.now();

  const after = await dumpNow(window, gw.origin, token);
  const mine = (r: TraceLine) => r.origin === 'client' && r.cid === after.cid && (r.seq ?? 0) > mark;
  const draws = after.lines.filter((r) => mine(r) && r.src === 'render' && r.kind === 'draw');
  const inWheel = draws.filter((r) => (r.ct ?? 0) >= wheelStart && (r.ct ?? 0) <= wheelEnd);
  const inPan = draws.filter((r) => (r.ct ?? 0) > wheelEnd && (r.ct ?? 0) <= panEnd);
  const ms = draws.map((r) => Number(r.kv?.ms ?? 0));
  const report = {
    tiles: ids.size,
    draws: draws.length,
    p50: pct(ms, 0.5),
    p95: pct(ms, 0.95),
    max: Math.max(...ms),
    wheelEvents: BURSTS * PER_BURST,
    wheelPaints: inWheel.length,
    paintsPerWheelEvent: inWheel.length / (BURSTS * PER_BURST),
    panPaints: inPan.length,
    firstBodiesMs,
    bodies: loaded.length,
  };
  console.log(`perf-many-texts ${JSON.stringify(report)}`);
  await testInfo.attach('perf', { body: JSON.stringify(report, null, 2), contentType: 'application/json' });
  expect(report.p95, `p95 paint ms over budget: ${JSON.stringify(report)}`).toBeLessThanOrEqual(P95_BUDGET_MS);
});
