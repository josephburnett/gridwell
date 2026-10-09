import { test, expect } from './fixtures';

// Ctrl or cmd (meta) + click on a tile descends in a new pane split below, in a
// focused pane or an unfocused one, and the clicked pane keeps its grid view.
// macOS reports a ctrl + left press as a right press with ctrl held, so that
// press, released without a drag, is the same gesture
// (gesture.RightClickIsSplitNav).
const presses = [
  { name: 'ctrl+click', key: 'Control', button: 'left' },
  { name: 'meta+click', key: 'Meta', button: 'left' },
  { name: 'ctrl+click as macOS reports it', key: 'Control', button: 'right' },
] as const;

for (const press of presses) {
  for (const unfocused of [false, true]) {
    test(`${press.name} on a well in ${unfocused ? 'an unfocused' : 'the focused'} pane descends in a split below`, async ({ gw }) => {
      await gw.enterPlugin('home');
      const a0 = await gw.focused();
      const cx = Math.round(a0.cx);
      const cy = Math.round(a0.cy) - 1;
      await gw.openPalette();
      await gw.dragCreate('well', cx, cy);

      if (unfocused) {
        await gw.splitFocusedPaneVertical();
        const b = (await gw.panes()).find((p) => p.id !== a0.id)!;
        await gw.clickScreen(b.x + 20, b.y + 20);
        expect((await gw.panes()).find((p) => p.id === b.id)!.focused, 'pane B focused').toBe(true);
      }
      const panesBefore = await gw.panes();
      const a = panesBefore.find((p) => p.id === a0.id)!;

      await gw.ctrlDescendCell(cx, cy, a.id, press.key, press.button);
      await expect.poll(async () => (await gw.panes()).length, {
        message: `the ${press.name} split the pane`,
      }).toBe(panesBefore.length + 1);

      const after = await gw.panes();
      const opened = after.find((p) => p.focused)!;
      const aNow = after.find((p) => p.id === a.id)!;
      expect(panesBefore.some((p) => p.id === opened.id), 'the new pane took focus').toBe(false);
      expect(opened.placeDepth, 'the new pane descended one doorway').toBe(a.placeDepth + 1);
      expect(opened.gridID, 'into the well child grid').not.toBe(a.gridID);
      expect(opened.y, 'the new pane is below the clicked one').toBeGreaterThan(aNow.y);
      expect(aNow.gridID, 'the clicked pane keeps its grid').toBe(a.gridID);
      expect(aNow.placeDepth, 'the clicked pane did not descend').toBe(a.placeDepth);
      expect([aNow.cx, aNow.cy, aNow.zoom], 'nor did its view move').toEqual([a.cx, a.cy, a.zoom]);
    });
  }
}
