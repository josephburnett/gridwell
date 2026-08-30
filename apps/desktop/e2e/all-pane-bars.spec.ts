import { test, expect } from './fixtures';

// Every pane wears the navigation bar, all the time. Focus shows only in the
// border color: a pane's bar geometry, and so its content box, which insets
// above the band unconditionally, is the same focused or not, so nothing
// resizes when focus moves. A band click in an unfocused pane moves focus and
// nothing else.

test('every pane wears the bar; moving focus moves nothing', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  await gw.splitFocusedPaneVertical();
  const panes = await gw.panes();
  expect(panes.length).toBe(2);
  const focused = panes.find((p) => p.focused)!;
  const other = panes.find((p) => !p.focused)!;

  // Both panes carry a live band with a chain.
  const barF1 = await gw.bar(focused.id);
  const barO1 = await gw.bar(other.id);
  expect(barF1.segments.length).toBeGreaterThan(0);
  expect(barO1.segments.length, 'an unfocused pane wears its own bar').toBeGreaterThan(0);
  expect(barO1.top, 'two side-by-side bands, two lefts').toBe(barF1.top);
  expect(barO1.left).not.toBe(barF1.left);

  // Click the unfocused pane's band dead center, over its title zone: focus
  // moves, and nothing else happens. No zoom toggle, no crumb ascent.
  await window.mouse.click(barO1.left + barO1.width / 2, barO1.top + barO1.height / 2);
  await expect.poll(async () => (await gw.panes()).find((p) => p.focused)?.id).toBe(other.id);
  expect((await gw.panes()).length, 'no zoom toggled, both panes still visible').toBe(2);

  // The focus move changed no bar geometry on either side, so no content
  // resizes when focus moves.
  const barF2 = await gw.bar(focused.id);
  const barO2 = await gw.bar(other.id);
  for (const [a, b] of [
    [barF1, barF2],
    [barO1, barO2],
  ] as const) {
    expect(b.top).toBe(a.top);
    expect(b.left).toBe(a.left);
    expect(b.width).toBe(a.width);
  }
});
