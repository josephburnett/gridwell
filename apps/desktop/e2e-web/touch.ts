import type { Page } from '@playwright/test';

// Raw CDP touch injection. page.touchscreen offers only tap, while the
// long-press and multi-finger vocabulary in client/touchgest needs
// Input.dispatchTouchEvent, so the browser fires real TouchEvents at the canvas,
// the same events a phone produces, and the whole touch-to-gesture pipeline runs
// rather than being simulated.

interface Pt {
  x: number;
  y: number;
}

async function session(page: Page) {
  return page.context().newCDPSession(page);
}

// longPressDrag holds one finger still past the touchgest HoldMs threshold,
// which classifies the press as the right button, then drags it: the touch form
// of every right-drag pane gesture, split, swap, clone, resize, ascend. The hold
// is a real wall-clock wait, because the long-press is a duration.
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


// pinch moves two fingers symmetrically about `center`, from fromHalf to toHalf
// of horizontal separation each way. Spreading, where toHalf is larger, zooms in.
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

// twoFingerTap taps two fingers briefly; touchgest maps it to a middle click,
// the ascend gesture.
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
