import { expect } from '@playwright/test';
import type { Page } from '@playwright/test';
import type { GridwellDriver } from './driver';

// The local echo scenarios the Electron and browser suites both run
// (shellconn.LocalEcho). A slow link is the client's shell output held by
// the test hook and released by the spec, so a typed key is drawn, or not,
// while its echo is known not to have arrived.

type Echo = {
  showing: boolean;
  delayMs: number;
  made: number;
  confirmed: number;
  wrong: number;
  pending: number;
  drawn: string;
  draws: number;
  inScreen: string;
};

// The link's latency while output is held: far above LocalEchoOnMs, so the
// measured delay turns showing on.
const LINK_MS = 200;

const hook = (window: Page, name: string): Promise<any> =>
  window.evaluate((n) => (window as any).__gridwellTest[n](), name);

export const echoOf = (window: Page): Promise<Echo> => hook(window, 'localEcho');
const shellText = (window: Page): Promise<string> => hook(window, 'shellText');

// openShell makes a shell on home and goes live in it, returning its cell.
export async function openShell(gw: GridwellDriver): Promise<{ cx: number; cy: number }> {
  await gw.enterSource('home');
  const home = await gw.focused();
  const cx = Math.round(home.cx);
  const cy = Math.round(home.cy);
  await gw.openMenu();
  await gw.dragCreate('shell', cx, cy);
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).textFocus, { timeout: 20_000 }).not.toBe('');
  await gw.shellAttached();
  return { cx, cy };
}

export async function closeShell(gw: GridwellDriver, at: { cx: number; cy: number }): Promise<void> {
  await gw.ascendViaCrumb();
  await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
  await gw.deleteTileCell(at.cx, at.cy);
}

// slowKey types key over the slow link and lets its echo land.
async function slowKey(window: Page, key: string): Promise<void> {
  await hook(window, 'holdShellOutput');
  await window.keyboard.type(key);
  await window.waitForTimeout(LINK_MS);
  await hook(window, 'releaseShellOutput');
  await expect.poll(async () => (await echoOf(window)).pending).toBe(0);
}

// No key is drawn while showing is off, and every key is measured. Whether
// this host's own path, tmux and the page's parse, is under LocalEchoOnMs
// depends on its load, so the threshold itself is the unit test's.
export async function fastLinkDrawsNothing(gw: GridwellDriver, window: Page): Promise<void> {
  for (const ch of 'echo fast-$((20+3))') {
    await window.keyboard.type(ch);
    const e = await echoOf(window);
    if (!e.showing) expect(e.drawn, `nothing drawn while not showing, delay ${e.delayMs} ms`).toBe('');
  }
  await window.keyboard.press('Enter');
  await gw.waitShellLine('fast-23');
  const e = await echoOf(window);
  expect(e.confirmed, 'every key was measured').toBeGreaterThan(0);
  if (e.draws === 0) expect(e.showing, `delay ${e.delayMs} ms`).toBe(false);
}

// The inked mean of row's cells c0 to c1 in a capture: the terminal's own
// canvas, cols by rows cells.
async function ink(window: Page, url: string, c0: number, c1: number, row: number): Promise<number> {
  return window.evaluate(
    async ([u, a, b, r]) => {
      const buf = (window as any).__gridwellTest.shellBuffer();
      const img = new Image();
      img.src = u as string;
      await img.decode();
      const cv = document.createElement('canvas');
      cv.width = img.width;
      cv.height = img.height;
      const g = cv.getContext('2d')!;
      g.drawImage(img, 0, 0);
      const cw = img.width / buf.cols;
      const ch = img.height / buf.rows;
      const d = g.getImageData(Math.round((a as number) * cw), Math.round((r as number) * ch),
        Math.round(((b as number) - (a as number)) * cw), Math.round(ch)).data;
      let sum = 0;
      for (let i = 0; i < d.length; i += 4) sum += d[i] + d[i + 1] + d[i + 2];
      return sum / (d.length / 4) / 3;
    },
    [url, c0, c1, row] as [string, number, number, number],
  );
}

// A slow link: a guess is drawn before its echo and the terminal does not
// hold it; the echo replaces it; a capture never holds it; and a key typed
// with echo off is never drawn.
export async function slowLink(window: Page, gw: GridwellDriver): Promise<void> {
  // One echoed key confirms the epoch; the delay is smoothed, so a fast
  // start takes a few to lift.
  let e: Echo;
  let i = 0;
  do {
    await slowKey(window, 'x');
    e = await echoOf(window);
  } while (++i < 8 && !e.showing);
  expect(e.showing, `a ${LINK_MS} ms link shows, delay ${e.delayMs}`).toBe(true);
  expect(e.inScreen).toContain('xterm-screen');

  await hook(window, 'holdShellOutput');
  await window.keyboard.type('y');
  await expect.poll(async () => (await echoOf(window)).drawn).toBe('y');
  expect(await shellText(window), 'the guess is not in the terminal').not.toContain('xy');
  await hook(window, 'releaseShellOutput');
  await expect.poll(() => shellText(window)).toContain('xy');
  await expect.poll(async () => (await echoOf(window)).drawn).toBe('');

  // The capture is the canvas alone: three guessed cells read as blank as
  // before they were typed, and as ink once the echo lands.
  const buf = await hook(window, 'shellBuffer');
  const [c0, c1, row] = [buf.cursorX + 1, buf.cursorX + 4, buf.cursorY];
  const before = await ink(window, await hook(window, 'shellCapture'), c0, c1, row);
  await hook(window, 'holdShellOutput');
  await window.keyboard.type('zzzz');
  await expect.poll(async () => (await echoOf(window)).drawn).toBe('zzzz');
  const guessed = await ink(window, await hook(window, 'shellCapture'), c0, c1, row);
  await hook(window, 'releaseShellOutput');
  await expect.poll(() => shellText(window)).toContain('xyzzzz');
  await expect.poll(async () => (await echoOf(window)).drawn).toBe('');
  // The terminal paints the echo a frame after it parses it.
  const inked = async () => Math.abs((await ink(window, await hook(window, 'shellCapture'), c0, c1, row)) - before);
  await expect.poll(inked, { message: 'the echo inks the cells' }).toBeGreaterThan(4);
  expect(Math.abs(guessed - before), 'a capture holds no guess').toBeLessThan((await inked()) / 4);

  // Echo off: read -s, typed over the slow link, is never drawn.
  await window.keyboard.press('Control+U');
  await expect.poll(() => shellText(window)).not.toContain('xyzzzz');
  await window.keyboard.type('read -s pw; echo got-${#pw}');
  await expect.poll(() => shellText(window)).toContain('echo got-');
  await expect.poll(async () => (await echoOf(window)).pending).toBe(0);
  await window.keyboard.press('Enter');
  const drawsBefore = (await echoOf(window)).draws;
  for (const ch of 'hunter2') {
    await hook(window, 'holdShellOutput');
    await window.keyboard.type(ch);
    await window.waitForTimeout(LINK_MS / 4);
    e = await echoOf(window);
    expect(e.drawn, 'a key typed with echo off is never drawn').toBe('');
    await hook(window, 'releaseShellOutput');
  }
  expect((await echoOf(window)).draws, 'nothing was drawn while echo was off').toBe(drawsBefore);
  await window.keyboard.press('Enter');
  await gw.waitShellLine('got-7');
  expect(await shellText(window)).not.toContain('hunter2');
}
