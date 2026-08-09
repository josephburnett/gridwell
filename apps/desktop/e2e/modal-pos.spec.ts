import { test, expect } from './fixtures';

// Pane-centered modals (issue #251): a dialog opens where you acted — the
// card centers on the ACTIVE pane, not the screen. One centering rule
// (panebox.ModalCardPos, applied by centerCardOnActivePane) covers every
// modal card; this spec crosses it through the url modal in a split, where
// pane-center and screen-center are far apart.
test('the url modal centers on the active pane, not the screen', async ({ gw, window }) => {
  await gw.enterPlugin('localdb');
  await gw.splitFocusedPaneVertical();
  const ps = (await gw.panes()).slice().sort((a: any, b: any) => a.x - b.x);
  const right = ps[1];
  await gw.focusPane(right);

  // Clicking (not dragging) the url swatch opens the ephemeral-visit modal
  // on the focused pane.
  await gw.clickPaletteSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });

  const card = await window.locator('#gw-url-form').boundingBox();
  expect(card, 'the modal card has layout').toBeTruthy();
  const cardCx = card!.x + card!.width / 2;
  const cardCy = card!.y + card!.height / 2;

  const winW = await window.evaluate(() => window.innerWidth);
  expect(
    Math.abs(cardCx - (right.x + right.w / 2)),
    'card centers on the focused pane horizontally',
  ).toBeLessThan(2);
  expect(
    Math.abs(cardCy - (right.y + right.h / 2)),
    'card centers on the focused pane vertically',
  ).toBeLessThan(2);
  // And the pane split makes that visibly different from screen centering.
  expect(
    Math.abs(cardCx - winW / 2),
    'pane-centered placement is not screen-centered in a split',
  ).toBeGreaterThan(50);

  await window.keyboard.press('Escape');
  await expect(window.locator('#gw-url-modal.open')).toHaveCount(0);
});
