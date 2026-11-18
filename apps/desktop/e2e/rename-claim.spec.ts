import { test, expect } from './fixtures';
import { renameTile, tileAt } from './oracle';

// A rename claims the version the user saw when the input opened, as every
// write of something the user made claims what it was read at. A name another
// writer set while the input was open is refused as a conflict and the row on
// disk shows, never overwritten unseen.

async function notices(window: any): Promise<{ source: string; message: string }[]> {
  return (await window.evaluate(() => (window as any).__gridwellTest.errors())).notices;
}

test('a rename typed over a foreign rename is refused and shows the row on disk', async ({ gw, window }) => {
  await gw.enterSource('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  await gw.openMenu();
  await gw.dragCreate('door', cx, cy);
  const door = tileAt(await gw.getGrid(home.gridID), 'door', cx, cy)!;
  await gw.descendCell(cx, cy);
  await gw.waitIdle();

  await gw.clickBarName('right');
  const input = window.locator('#gw-rename-input');
  await expect(input).toBeVisible();

  // An agent renames the door while the input is open; the event reaches this
  // client's cache before Enter.
  await renameTile(gw.origin, door.id, 'agent');
  await window.waitForTimeout(1000);

  await input.fill('mine');
  await input.press('Enter');

  await gw.waitIdle();
  expect(String(tileAt(await gw.getGrid(home.gridID), 'door', cx, cy)?.altText ?? ''), 'the agent name stands').toBe(
    'agent',
  );
  await expect
    .poll(async () => (await notices(window)).some((n) => n.source === 'conflict:Rename'), {
      message: 'the refused rename is told',
    })
    .toBe(true);
  await expect.poll(async () => (await gw.barName()).label, { message: 'the bar shows the row on disk' }).toBe('agent');
});

test('a workspace rename claims the version its level was read at', async ({ gw, window }) => {
  await gw.enterSource('home');
  const f = await gw.focused();
  const wx = Math.round(f.cx);
  const wy = Math.round(f.cy);
  await gw.openMenu();
  await gw.dragCreate('workspace', wx, wy);
  const ws = tileAt(await gw.getGrid(f.gridID), 'workspace', wx, wy)!;
  await gw.descendCell(wx, wy);
  await expect
    .poll(async () => (await window.evaluate(() => (window as any).__gridwellTest.workspace())).depth)
    .toBe(1);
  await gw.waitIdle();

  const bar = await window.evaluate(() => (window as any).__gridwellTest.bar());
  const crumb = bar.segments.find((s: any) => s.kind === 'workspace' && s.level === 1);
  await window.mouse.click(crumb.x + 20, bar.top + bar.height / 2, { button: 'right' });
  const input = window.locator('#gw-rename-input');
  await expect(input).toBeVisible();

  await renameTile(gw.origin, ws.id, 'agent');
  await window.waitForTimeout(1000);

  await input.fill('mine');
  await input.press('Enter');

  await gw.waitIdle();
  expect(String(tileAt(await gw.getGrid(f.gridID), 'workspace', wx, wy)?.altText ?? ''), 'the agent name stands').toBe(
    'agent',
  );
  await expect
    .poll(async () => (await notices(window)).some((n) => n.source === 'conflict:Rename'), {
      message: 'the refused rename is told',
    })
    .toBe(true);
  await gw.leaveWorkspace();
});
