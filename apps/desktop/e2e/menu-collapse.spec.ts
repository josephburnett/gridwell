import { test, expect } from './fixtures';

// The + menu's swatch section is an open set, since a node declares as many
// roots and connections as it likes and a row declares several grids, so it
// is folded away behind a chevron strip and a menu opens on the four
// primitives. This crosses what menu.Show cannot see on its own: the
// composition decision, the popover geometry it moves, the press that works the
// strip, and client/menu's fold flag dying with the menu.

test('the + menu opens with the swatch section folded, and the chevron works it', async ({
  gw,
  window,
}) => {
  await gw.sources();
  const before = await gw.focused();
  await gw.openMenu();

  // Collapsed: the primitives and a chevron pointing up at the section above.
  const folded = await gw.menu();
  expect(folded.toggle.present, 'the folded section offers a control').toBe(true);
  expect(folded.toggle.chevron, 'it points up: press to open the section above').toBe('up');
  expect(folded.toggle.expanded, 'and the section is not shown').toBe(false);
  expect(
    folded.items.some((i) => i.isSwatch),
    'no swatch is in the popover — nothing to click, drag or hover',
  ).toBe(false);
  const primitives = folded.items.filter((i) => !i.strip);
  expect(primitives.length, 'the regular tiles are there').toBeGreaterThanOrEqual(4);
  // The strip is inside the popover and on no swatch, so a press there belongs
  // to the menu rather than the canvas underneath it.
  for (const p of primitives) {
    expect(p.y, 'every primitive sits below the strip').toBeGreaterThanOrEqual(
      folded.toggle.y + folded.toggle.h,
    );
  }

  // Press the strip: the section opens above the primitives, and nothing else
  // moves. Same focused pane, same open menu, same selection.
  await window.mouse.click(
    folded.toggle.x + folded.toggle.w / 2,
    folded.toggle.y + folded.toggle.h / 2,
  );
  await gw.waitIdle();
  const open = await gw.menu();
  expect(open.open, 'the menu is still open').toBe(true);
  expect(open.toggle.chevron, 'the chevron now folds it back').toBe('down');
  expect(open.toggle.expanded).toBe(true);
  const swatches = open.items.filter((i) => i.isSwatch);
  expect(swatches.length, 'the section shows its swatches').toBeGreaterThan(0);
  expect(
    Math.max(...swatches.map((i) => i.y)),
    'the section sits above the primitives',
  ).toBeLessThan(Math.min(...open.items.filter((i) => !i.strip).map((i) => i.y)));
  const still = await gw.focused();
  expect(still.id, 'working the chevron moved no focus').toBe(before.id);
  expect(still.gridID, 'and descended nowhere').toBe(before.gridID);

  // Press it again: folded, with the primitives back where they opened.
  await window.mouse.click(
    open.toggle.x + open.toggle.w / 2,
    open.toggle.y + open.toggle.h / 2,
  );
  await gw.waitIdle();
  const refolded = await gw.menu();
  expect(refolded.toggle.chevron, 'folded again').toBe('up');
  expect(refolded.items.some((i) => i.isSwatch), 'the section is hidden again').toBe(false);
  expect(refolded.items.map((i) => i.kind), 'the same primitives, in the same place').toEqual(
    primitives.map((i) => i.kind),
  );
  expect(refolded.open, 'and the menu never closed').toBe(true);
});

test('an expanded entry still descends, and the next menu opens folded again', async ({ gw }) => {
  const pls = await gw.sources();
  const home = pls.find((p) => p.label === 'home')!;
  const before = await gw.focused();

  // Expanded, a swatch click descends into the grid it roots.
  await gw.openMenu();
  await gw.expandSources();
  await gw.clickSwatch('home');
  const inside = await gw.focused();
  expect(inside.gridID, 'the expanded swatch descended').toBe(home.rootGridID);

  // The fold is the menu's own live state and dies with it: the menu that
  // opens after the descent is folded, whatever the last one was left as.
  await gw.openMenu();
  const next = await gw.menu();
  expect(next.toggle.expanded, 'every opening starts folded').toBe(false);
  expect(next.toggle.chevron).toBe('up');

  await gw.ascendViaCrumb();
  expect((await gw.focused()).gridID, 'ascent returns to the origin grid').toBe(before.gridID);
});
