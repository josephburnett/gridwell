import { test, expect } from './fixtures';

// The + menu's plugin and connection section is an open set — a node declares
// as many as it likes and one plugin declares several entries — so it is
// folded away behind a chevron strip and the four primitives are what a menu
// opens on. This crosses the seam palette.Show cannot see on its own: the
// composition decision, the popover geometry it moves, the press that works
// the strip, and client/menu's fold flag dying with the menu.

test('the + menu opens with the plugin section folded, and the chevron works it', async ({
  gw,
  window,
}) => {
  await gw.plugins();
  const before = await gw.focused();
  await gw.openPalette();

  // Collapsed: the primitives and a chevron pointing up at the section above.
  const folded = await gw.palette();
  expect(folded.toggle.present, 'the folded section offers a control').toBe(true);
  expect(folded.toggle.chevron, 'it points up: press to open the section above').toBe('up');
  expect(folded.toggle.expanded, 'and the section is not shown').toBe(false);
  expect(
    folded.items.some((i) => i.isPlugin),
    'no plugin swatch is in the popover — nothing to click, drag or hover',
  ).toBe(false);
  const primitives = folded.items.filter((i) => !i.isPlugin);
  expect(primitives.length, 'the regular tiles are there').toBeGreaterThanOrEqual(4);
  // The strip is inside the popover and on no swatch: a press there is the
  // menu's, never the canvas underneath it.
  for (const p of primitives) {
    expect(p.y, 'every primitive sits below the strip').toBeGreaterThanOrEqual(
      folded.toggle.y + folded.toggle.h,
    );
  }

  // Press the strip: the section opens above the primitives, and nothing else
  // moves — same focused pane, same open menu, same selection.
  await window.mouse.click(
    folded.toggle.x + folded.toggle.w / 2,
    folded.toggle.y + folded.toggle.h / 2,
  );
  await gw.waitIdle();
  const open = await gw.palette();
  expect(open.open, 'the menu is still open').toBe(true);
  expect(open.toggle.chevron, 'the chevron now folds it back').toBe('down');
  expect(open.toggle.expanded).toBe(true);
  const plugins = open.items.filter((i) => i.isPlugin);
  expect(plugins.length, 'the section shows its entries').toBeGreaterThan(0);
  expect(
    Math.max(...plugins.map((i) => i.y)),
    'the section sits above the primitives',
  ).toBeLessThan(Math.min(...open.items.filter((i) => !i.isPlugin).map((i) => i.y)));
  const still = await gw.focused();
  expect(still.id, 'working the chevron moved no focus').toBe(before.id);
  expect(still.gridID, 'and descended nowhere').toBe(before.gridID);

  // Press it again: folded, with the primitives back where they opened.
  await window.mouse.click(
    open.toggle.x + open.toggle.w / 2,
    open.toggle.y + open.toggle.h / 2,
  );
  await gw.waitIdle();
  const refolded = await gw.palette();
  expect(refolded.toggle.chevron, 'folded again').toBe('up');
  expect(refolded.items.some((i) => i.isPlugin), 'the section is hidden again').toBe(false);
  expect(refolded.items.map((i) => i.kind), 'the same primitives, in the same place').toEqual(
    primitives.map((i) => i.kind),
  );
  expect(refolded.open, 'and the menu never closed').toBe(true);
});

test('an expanded entry still descends, and the next menu opens folded again', async ({ gw }) => {
  const pls = await gw.plugins();
  const home = pls.find((p) => p.label === 'home')!;
  const before = await gw.focused();

  // Expanded, a swatch behaves exactly as it did when the section was always
  // shown: a click descends into the grid it roots.
  await gw.openPalette();
  await gw.expandPlugins();
  await gw.clickPluginSwatch('home');
  const inside = await gw.focused();
  expect(inside.gridID, 'the expanded swatch descended').toBe(home.rootGridID);

  // The fold is the menu's own live state and dies with it: the menu that
  // opens after the descent is folded, whatever the last one was left as.
  await gw.openPalette();
  const next = await gw.palette();
  expect(next.toggle.expanded, 'every opening starts folded').toBe(false);
  expect(next.toggle.chevron).toBe('up');

  await gw.ascendViaCrumb();
  expect((await gw.focused()).gridID, 'ascent returns to the origin grid').toBe(before.gridID);
});
