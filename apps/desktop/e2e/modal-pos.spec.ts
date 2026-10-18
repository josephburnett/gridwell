import { test, expect } from './fixtures';

// A modal card centers on the active pane. panebox.ModalCardPos, applied by
// centerCardOnActivePane, is the one rule for every modal card. The url modal
// in a split is where pane center and screen center are far apart.
test('the url modal centers on the active pane, not the screen', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  await gw.splitFocusedPaneVertical();
  const ps = (await gw.panes()).slice().sort((a: any, b: any) => a.x - b.x);
  const right = ps[1];
  await gw.focusPane(right);

  // Clicking the swatch, rather than dragging it, opens the modal.
  await gw.clickMenuSwatch('url');
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });

  const card = await window.locator('#gw-url-form').boundingBox();
  expect(card, 'the modal card has layout').toBeTruthy();
  const cardCx = card!.x + card!.width / 2;
  const cardCy = card!.y + card!.height / 2;

  const winW = await window.evaluate(() => globalThis.innerWidth);
  expect(
    Math.abs(cardCx - (right.x + right.w / 2)),
    'card centers on the focused pane horizontally',
  ).toBeLessThan(2);
  expect(
    Math.abs(cardCy - (right.y + right.h / 2)),
    'card centers on the focused pane vertically',
  ).toBeLessThan(2);
  expect(
    Math.abs(cardCx - winW / 2),
    'pane-centered placement is not screen-centered in a split',
  ).toBeGreaterThan(50);

  await window.keyboard.press('Escape');
  await expect(window.locator('#gw-url-modal.open')).toHaveCount(0);
});
