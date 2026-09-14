import type { Page } from '@playwright/test';

// Timing a debounce from both sides. A debounced write cannot reach its
// observable before the wait elapses from the arm, so timing from just before
// the arm gives a lower bound the cadence itself forces, and the ceiling comes
// from the same value; a spec that reads both out of client/cadence through the
// hook can never sleep less than the wait it is waiting on. Two conditions make
// the measurement honest and both are the caller's: nothing may be armed
// already, which settle ensures, and arm must be one quick act, or the poll
// starts after the wait has already run out and any cadence passes.

// settle lets an already-pending wait fire and leaves the client quiet. The
// framing and layout debounces re-arm from draw(), so a spec that times one
// without this would be timing a wait armed before it looked.
export async function settle(page: Page, cadenceMs: number): Promise<void> {
  await page.waitForTimeout(cadenceMs * 2);
}

// timeToLand runs arm and returns, per probe, the milliseconds from just before
// arm to that probe's first true. It throws naming the probes that never
// landed, so a stalled cadence reports which observable is missing.
export async function timeToLand(
  page: Page,
  arm: () => Promise<void>,
  probes: Record<string, () => Promise<boolean>>,
  ceilingMs: number,
): Promise<Record<string, number>> {
  const landed: Record<string, number> = {};
  const names = Object.keys(probes);
  const t0 = Date.now();
  await arm();
  for (;;) {
    for (const name of names) {
      if (landed[name] === undefined && (await probes[name]())) landed[name] = Date.now() - t0;
    }
    if (names.every((n) => landed[n] !== undefined)) return landed;
    if (Date.now() - t0 > ceilingMs) {
      const missing = names.filter((n) => landed[n] === undefined);
      throw new Error(`did not land within ${ceilingMs}ms: ${missing.join(', ')}`);
    }
    await page.waitForTimeout(25);
  }
}
