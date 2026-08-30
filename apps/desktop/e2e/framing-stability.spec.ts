import { test, expect } from './fixtures';
import { tileAt, placeTile } from './oracle';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';

// The framing-audit program (decisions 2026-08-13): saves survive races,
// unloads, and sibling panes; every root grid persists its viewport.

const FS_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'gridwell-framing-'));
test.use({ extraPlugins: [{ kind: 'fs', name: 'pics', config: { root: FS_ROOT } }] });

// Gap 9: a framing save that races a version-bumping write retries with a
// fresh claim instead of silently dropping.
test('a framing save survives a racing version bump', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  await gw.descendCell(cx, cy);
  await gw.wheelAtFocusedCenter(-240); // reframe the child
  // Race: a foreign placement bumps the WELL's version inside the settle
  // window, so the framing flush's claim is stale.
  const well = tileAt(await gw.getGrid(home.gridID), 'well', cx, cy)!;
  await placeTile(gw.origin, well.id, well.version as number, home.gridID, cx, cy, 2, 2);
  await expect
    .poll(
      async () =>
        Number(
          (tileAt(await gw.getGrid(home.gridID), 'well', cx, cy) as { viewZoom?: number | string })
            ?.viewZoom ?? 0,
        ),
      { timeout: 10_000 },
    )
    .toBeGreaterThan(0);
  void window;
});

// Gaps 3/4 class: an fs plugin ROOT persists its viewport (the root framing write was
// silently swallowed by fs/proc before).
test('an fs root grid keeps its viewport across leave and re-entry', async ({ gw }) => {
  await gw.enterPlugin('pics');
  const grid = (await gw.focused()).gridID;
  await gw.wheelAtFocusedCenter(-240);
  const zc = await gw.focused();
  await gw.panFocusedGrid(Math.round(zc.cx), Math.round(zc.cy), Math.round(zc.cx) - 1, Math.round(zc.cy));
  const left = await gw.focused();
  await gw.ascendViaCrumb();
  await gw.enterPlugin('pics');
  const back = await gw.focused();
  expect(back.gridID).toBe(grid);
  expect(back.zoom, 'fs root zoom restored').toBeCloseTo(left.zoom, 1);
  expect(Math.abs(back.cx - left.cx), 'fs root cx restored').toBeLessThan(0.51);
});

// Gaps 7/8: a reload fired INSIDE the settle window still lands the save —
// the unload flush rides beacons that outlive the page.
test('a reload inside the settle window does not lose the framing', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  await gw.descendCell(cx, cy);
  await gw.wheelAtFocusedCenter(-240);
  // No settle wait: reload immediately. The beacon must land server-side.
  await window.reload();
  await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
  await expect
    .poll(
      async () =>
        Number(
          (tileAt(await gw.getGrid(home.gridID), 'well', cx, cy) as { viewZoom?: number | string })
            ?.viewZoom ?? 0,
        ),
      { timeout: 10_000 },
    )
    .toBeGreaterThan(0);
});

// Gap 5: text scroll persists on the settle tick, not just at ascent.
test('text scroll persists without an ascent', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const doc = tileAt(await gw.getGrid(home.gridID), 'text', cx, cy)!;
  const { updateText } = await import('./oracle');
  await updateText(gw.origin, doc.id, Number(doc.version ?? 0), '# long\n\n' + 'line\n\n'.repeat(200));
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).textFocus).not.toBe('');
  // Rendered mode: the wheel scrolls the document.
  await window.mouse.move(home.x + home.w / 2, home.y + home.h / 2);
  for (let i = 0; i < 8; i++) await window.mouse.wheel(0, 120);
  await expect
    .poll(
      async () =>
        Number(
          (tileAt(await gw.getGrid(home.gridID), 'text', cx, cy) as { textY?: number | string })
            ?.textY ?? 0,
        ),
      { message: 'the scroll reached server truth with NO ascent', timeout: 10_000 },
    )
    .toBeGreaterThan(0);
});

// Gap 6: one active surface per grid — a passive sibling pane never
// overwrites the focused pane's persisted framing.
test('a split sibling never overwrites the focused pane framing', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  await gw.openPalette();
  await gw.dragCreate('well', cx, cy);
  await gw.descendCell(cx, cy);
  await gw.waitIdle();
  // Split: two panes now show the SAME child grid with different rects.
  await gw.splitFocusedPaneVertical();
  await gw.waitIdle();
  // Reframe the FOCUSED pane and let it persist.
  await gw.wheelAtFocusedCenter(-240);
  const well = () => gw.getGrid(home.gridID).then((g) => tileAt(g, 'well', cx, cy)!);
  await expect
    .poll(async () => Number((await well()).viewZoom ?? 0), { timeout: 10_000 })
    .toBeGreaterThan(0.125);
  const settled = await well();
  // Provoke more settle ticks (draws) without touching either viewport:
  // the PASSIVE sibling must not write its own rect-derived framing back.
  for (let i = 0; i < 3; i++) {
    await window.mouse.move(100 + i, 100);
    await window.waitForTimeout(800);
  }
  const after = await well();
  expect(after.viewZoom, 'sibling must not overwrite (one active surface)').toEqual(settled.viewZoom);
  expect(after.viewX, 'origin stable').toEqual(settled.viewX);
});
