import type { Page } from '@playwright/test';

// Raw CDP touch injection. page.touchscreen offers only tap; the long-press
// and multi-finger vocabulary (client/touchgest) needs Input.dispatchTouchEvent
// so the browser fires REAL TouchEvents at the canvas — the same events an
// iPhone produces — and the whole touch→gesture pipeline is exercised, not
// simulated.

interface Pt {
  x: number;
  y: number;
}

async function session(page: Page) {
  return page.context().newCDPSession(page);
}

// longPressDrag holds one finger still past the touchgest HoldMs threshold
// (classifying the press as the right button), then drags it — the touch form
// of every right-drag pane gesture (split / swap / clone / resize / ascend).
// The hold is a real wall-clock wait: the long-press IS a duration.
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

// longPressInPlace holds one finger still past the HoldMs threshold and lifts
// without moving — the touch form of a bare right CLICK (ascend on the corner
// circle, pane zoom on the name pill). Distinct from longPressDrag: the DOM
// overlay buttons act on the right mousedown the hold synthesizes, so the
// press must land on the element and stay put.
export async function longPressInPlace(page: Page, at: Pt, holdMs = 550): Promise<void> {
  const s = await session(page);
  await s.send('Input.dispatchTouchEvent', {
    type: 'touchStart',
    touchPoints: [{ x: at.x, y: at.y }],
  });
  await page.waitForTimeout(holdMs);
  await s.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
  await s.detach();
}

// pinch moves two fingers symmetrically about `center` from ±fromHalf to
// ±toHalf horizontal separation: spread (toHalf > fromHalf) zooms in.
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

// twoFingerTap taps two fingers briefly — touchgest maps it to a middle click
// (the ascend gesture).
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
