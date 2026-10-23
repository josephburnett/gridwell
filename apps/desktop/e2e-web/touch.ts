import type { Page } from '@playwright/test';

// Raw CDP touch injection. page.touchscreen offers only tap, while the
// long-press and multi-finger vocabulary in client/touchgest needs
// Input.dispatchTouchEvent, which fires the TouchEvents a phone produces.

interface Pt {
  x: number;
  y: number;
}

async function session(page: Page) {
  return page.context().newCDPSession(page);
}

// Holds one finger past the touchgest HoldMs threshold, which classifies the
// press as the right button, then drags: the touch form of every right-drag
// pane gesture. The hold is a real wall-clock wait.
export async function longPressDrag(page: Page, from: Pt, to: Pt, holdMs = 550): Promise<void> {
  const s = await session(page);
  await s.send('Input.dispatchTouchEvent', {
    type: 'touchStart',
    touchPoints: [{ x: from.x, y: from.y }],
  });
  await page.waitForTimeout(holdMs);
  const steps = 8;
  for (let i = 1; i <= steps; i++) {
    await s.send('Input.dispatchTouchEvent', {
      type: 'touchMove',
      touchPoints: [
        { x: from.x + ((to.x - from.x) * i) / steps, y: from.y + ((to.y - from.y) * i) / steps },
      ],
    });
  }
  await s.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
  await s.detach();
}

// Holds one finger past HoldMs and lifts it where it landed: a right click, and
// on a tile touchgest's Select toggle.
export async function longPress(page: Page, at: Pt, holdMs = 550): Promise<void> {
  const s = await session(page);
  await s.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x: at.x, y: at.y }] });
  await page.waitForTimeout(holdMs);
  await s.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
  await s.detach();
}

// Two fingers symmetrically about `center`, from fromHalf to toHalf of
// separation each way. Spreading zooms in.
export async function pinch(page: Page, center: Pt, fromHalf: number, toHalf: number): Promise<void> {
  const s = await session(page);
  const at = (half: number) => [
    { x: center.x - half, y: center.y },
    { x: center.x + half, y: center.y },
  ];
  await s.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: at(fromHalf) });
  const steps = 10;
  for (let i = 1; i <= steps; i++) {
    await s.send('Input.dispatchTouchEvent', {
      type: 'touchMove',
      touchPoints: at(fromHalf + ((toHalf - fromHalf) * i) / steps),
    });
  }
  await s.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
  await s.detach();
}

// touchgest maps a brief two-finger tap to a middle click, the ascend.
export async function twoFingerTap(page: Page, center: Pt): Promise<void> {
  const s = await session(page);
  await s.send('Input.dispatchTouchEvent', {
    type: 'touchStart',
    touchPoints: [
      { x: center.x - 20, y: center.y },
      { x: center.x + 20, y: center.y },
    ],
  });
  await page.waitForTimeout(60);
  await s.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
  await s.detach();
}
