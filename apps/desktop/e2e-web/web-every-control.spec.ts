import * as fs from 'node:fs';
import * as path from 'node:path';
import type { Locator, Page } from '@playwright/test';
import { test, expect, Served } from './fixtures';
import { GridwellDriver } from '../e2e/driver';
import { makeRunDir } from '../e2e/homes';
import { tileAt } from '../e2e/oracle';
import { longPress } from './touch';

// Every control in test/boundary/controls.txt, pressed with a finger at a
// phone's size and with a mouse at a desktop's: each row is brought on screen,
// pressed, and held to what the row says a press does, and the client must
// still be running after (Joe, 2026-11-22: "Instead we should have tests for
// these cases"). The cases are keyed by row name; TestEveryControlIsARow fails
// a row with no case, and this file fails a case with no row.

type Device = 'finger' | 'mouse';
interface Pt {
  x: number;
  y: number;
}

interface Row {
  name: string;
  press: string;
  devices: string;
}

const TABLE = path.resolve(__dirname, '..', '..', '..', 'test', 'boundary', 'controls.txt');

function controls(): Row[] {
  return fs
    .readFileSync(TABLE, 'utf8')
    .split('\n')
    .filter((l) => l.trim() !== '' && !l.startsWith('#'))
    .map((l) => l.split(' | '))
    .map(([name, , press, devices]) => ({ name, press, devices }))
    .filter((r) => !r.devices.startsWith('not a control'));
}

const VIEWPORT: Record<Device, { width: number; height: number }> = {
  finger: { width: 390, height: 844 },
  mouse: { width: 1024, height: 768 },
};

interface Ctx {
  gw: GridwellDriver;
  window: Page;
  serve: Served;
  device: Device;
  // The device's plain press: a finger's tap, a mouse's left click.
  press(at: Pt): Promise<void>;
  // The device's right press: a finger's long-press, a mouse's right click.
  pressRight(at: Pt): Promise<void>;
  // press at the middle of what el covers on screen.
  pressOn(el: Locator): Promise<void>;
}

interface Case {
  use?: { filesRoots?: { root: string; label: string }[]; extraYaml?: string };
  run(c: Ctx): Promise<void>;
}

const FILES = makeRunDir();
fs.writeFileSync(path.join(FILES, 'main.go'), 'package main\n');
fs.writeFileSync(path.join(FILES, 'firmware.bin'), Buffer.alloc(300, 0x41));
const filesRoots = [{ root: FILES, label: 'code' }];

const notices = async (window: Page): Promise<any[]> =>
  (await window.evaluate(() => (window as any).__gridwellTest.errors())).notices ?? [];
const keyboard = (window: Page): Promise<{ owner: string; pane: string; active: string }> =>
  window.evaluate(() => (window as any).__gridwellTest.keyboard());
const shown = (window: Page, id: string): Promise<boolean> =>
  window.evaluate((id) => {
    const el = document.getElementById(id) as HTMLElement | null;
    return !!el && el.offsetParent !== null;
  }, id);
const center = async (el: Locator): Promise<Pt> => {
  await el.waitFor({ state: 'visible', timeout: 10_000 });
  const b = (await el.boundingBox())!;
  return { x: b.x + b.width / 2, y: b.y + b.height / 2 };
};

// A text tile made on home's root at the focused pane's center and descended
// into raw, holding body.
async function newText(c: Ctx, body: string): Promise<{ id: string; gridID: string; cx: number; cy: number }> {
  const { gw } = c;
  await gw.enterSource('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openMenu();
  await gw.dragCreate('markdown', cx, cy);
  const t = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.focused()).textMode).toBe('text');
  if (body !== '') {
    await gw.pasteText(body);
    await expect.poll(() => gw.getTileContent(t.id), { timeout: 10_000 }).toBe(body);
  }
  return { id: t.id, gridID: f.gridID, cx, cy };
}

async function rendered(c: Ctx, body: string) {
  const t = await newText(c, body);
  await c.gw.toggleTextMode();
  await expect.poll(async () => (await c.gw.focused()).textMode).toBe('rendered');
  return t;
}

// A file of the files root, descended into.
async function openFile(c: Ctx, name: string): Promise<void> {
  const { gw } = c;
  await gw.enterSource('code (files)');
  const f = await gw.focused();
  const t = ((await gw.getGrid(f.gridID)).tiles ?? []).find((t) => t.altText === name)!;
  await gw.descendCell(Number(t.x ?? 0), Number(t.y ?? 0));
  await expect.poll(async () => (await gw.focused()).textFocus).not.toBe('');
}

// A url tile dropped on home's root at the focused pane's center plus dx, and
// descended into, which asks for its address in the entry modal.
async function urlAsking(c: Ctx, dx = 0): Promise<{ gridID: string; cx: number; cy: number }> {
  const { gw, window } = c;
  if (dx === 0) await gw.enterSource('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx) + dx;
  const cy = Math.round(f.cy);
  await gw.openMenu();
  await gw.dragCreate('url', cx, cy);
  await gw.descendCell(cx, cy);
  await window.locator('#gw-url-modal.open').waitFor({ timeout: 5_000 });
  return { gridID: f.gridID, cx, cy };
}

const urlOf = async (c: Ctx, u: { gridID: string; cx: number; cy: number }) =>
  tileAt(await c.gw.getGrid(u.gridID), 'url', u.cx, u.cy)?.urlString ?? '';

// A door on home's root, descended into, so the bar's name is the door's.
async function doorDescent(c: Ctx): Promise<{ gridID: string; cx: number; cy: number }> {
  const { gw } = c;
  await gw.enterSource('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openMenu();
  await gw.dragCreate('door', cx, cy);
  await gw.descendCell(cx, cy);
  await expect.poll(async () => (await gw.barName()).editable).toBe(true);
  return { gridID: f.gridID, cx, cy };
}

async function openRename(c: Ctx): Promise<Locator> {
  const b = await c.gw.barName();
  await c.pressRight({ x: b.x + b.w / 2, y: b.top + b.height / 2 });
  const input = c.window.locator('#gw-rename-input');
  await expect(input).toBeVisible();
  return input;
}

async function openCircleMenu(c: Ctx): Promise<Locator> {
  await c.gw.enterSource('home');
  await c.pressRight(await c.gw.circleCenter());
  const popover = c.window.locator('#gw-circle-menu');
  await popover.waitFor({ timeout: 5_000 });
  return popover;
}

// The middle of the focused pane, the grid's background on a fresh grid.
async function paneMiddle(c: Ctx): Promise<Pt> {
  const f = await c.gw.focused();
  return { x: f.x + f.w / 2, y: f.y + f.h / 3 };
}

const cases: Record<string, Case> = {
  '+ circle': {
    async run(c) {
      await c.gw.enterSource('home');
      const m = await c.gw.menu();
      expect(m.open, 'the menu starts closed').toBe(false);
      await c.press({ x: m.plusX, y: m.plusY });
      await expect.poll(async () => (await c.gw.menu()).open).toBe(true);
    },
  },

  'notice row': {
    async run(c) {
      await c.gw.enterSource('home');
      await c.gw.rightClickCircle();
      await c.window.locator('#gw-circle-menu [data-gw-choice="dump"]').click();
      await expect.poll(async () => (await notices(c.window)).length, { timeout: 20_000 }).toBeGreaterThan(0);
      const n = (await notices(c.window))[0];
      expect(n.button, 'the dump notice has no button').toBeFalsy();
      const h = c.window.viewportSize()!.height;
      await c.press({ x: 10, y: h - 5 });
      await expect.poll(async () => (await notices(c.window)).length).toBe(0);
    },
  },

  'notice Reload': {
    async run(c) {
      const { gw, window } = c;
      await gw.enterSource('home');
      let on = true;
      await window.route('**/gridwell.v1.Gridwell/**', async (route) => {
        if (!on) return route.continue();
        await route.continue({ headers: { ...route.request().headers(), 'gridwell-build': 'e2e-old-build' } });
      });
      const f = await gw.focused();
      await gw.panFocusedGrid(Math.round(f.cx) + 1, Math.round(f.cy), Math.round(f.cx) - 1, Math.round(f.cy));
      const build = async () => (await notices(window)).find((n) => n.source === 'build');
      await expect.poll(async () => (await build())?.button, { timeout: 30_000 }).toBeTruthy();
      window.once('load', () => (on = false));
      const reloaded = window.waitForEvent('load', { timeout: 60_000 });
      const b = (await build())!.button;
      await c.press({ x: b.x + b.w / 2, y: b.y + b.h / 2 });
      await reloaded;
      await window.waitForFunction(() => !!(window as any).__gridwellTest, null, { timeout: 30_000 });
      expect(
        await window.evaluate(() => (performance.getEntriesByType('navigation')[0] as PerformanceNavigationTiming).type),
      ).toBe('reload');
    },
  },

  'notice Disable': {
    use: { extraYaml: 'connections:\n  - name: awaycon\n    label: away\n    addr: 127.0.0.1:1\n' },
    async run(c) {
      const { gw, window } = c;
      await gw.enterSource('home');
      const away = (await gw.sources()).find((p) => p.label === 'away')!;
      const health = async () => (await notices(window)).find((n) => n.source === 'source:' + away.uuid);
      await expect.poll(async () => (await health())?.button, { timeout: 30_000 }).toBeTruthy();
      const b = (await health()).button;
      await c.press({ x: b.x + b.w / 2, y: b.y + b.h / 2 });
      await expect.poll(health, { timeout: 15_000 }).toBeUndefined();
      expect((await notices(window)).filter((n) => n.source === 'rpc:DisableSource'), 'the verb was taken').toEqual([]);
    },
  },

  'text toggle': {
    async run(c) {
      await newText(c, '');
      const m = await c.gw.menu();
      await c.press({ x: m.plusX, y: m.plusY });
      await expect.poll(async () => (await c.gw.focused()).textMode).toBe('rendered');
      await c.press({ x: m.plusX, y: m.plusY });
      await expect.poll(async () => (await c.gw.focused()).textMode).toBe('text');
    },
  },

  'info toggle': {
    use: { filesRoots },
    async run(c) {
      await openFile(c, 'main.go');
      await expect.poll(() => c.gw.textareaValue(), { timeout: 10_000 }).toBe('package main\n');
      const btn = c.window.locator('#gw-info-toggle');
      await c.pressOn(btn);
      await expect.poll(async () => (await c.gw.focused()).textMode).toBe('info');
      await expect.poll(() => shown(c.window, 'gw-file-info')).toBe(true);
      await c.pressOn(btn);
      await expect.poll(async () => (await c.gw.focused()).textMode).not.toBe('info');
      await expect.poll(() => shown(c.window, 'gw-file-info')).toBe(false);
    },
  },

  'see more': {
    use: { filesRoots },
    async run(c) {
      await openFile(c, 'firmware.bin');
      await expect.poll(() => shown(c.window, 'gw-file-info-table'), { timeout: 10_000 }).toBe(true);
      expect(await shown(c.window, 'gw-file-hex'), 'the hex view waits for see more').toBe(false);
      const more = c.window.locator('#gw-file-see-more');
      await c.pressOn(more);
      await expect.poll(() => shown(c.window, 'gw-file-hex')).toBe(true);
      await c.pressOn(more);
      await expect.poll(() => shown(c.window, 'gw-file-hex')).toBe(false);
    },
  },

  editor: {
    async run(c) {
      await newText(c, 'aaaa\nbbbb');
      const at = await c.window.evaluate(() => {
        const ta = document.getElementById('gw-text-editor') as HTMLTextAreaElement;
        const r = ta.getBoundingClientRect();
        const s = getComputedStyle(ta);
        return {
          caret: ta.selectionStart,
          x: r.left + parseFloat(s.paddingLeft) + 2,
          y: r.top + parseFloat(s.paddingTop) + parseFloat(s.lineHeight) / 2,
        };
      });
      expect(at.caret, 'the paste left the caret at the end').toBe(9);
      await c.press(at);
      await expect
        .poll(() => c.window.evaluate(() => (document.getElementById('gw-text-editor') as HTMLTextAreaElement).selectionStart))
        .toBeLessThanOrEqual(1);
      expect(await keyboard(c.window)).toMatchObject({ owner: 'editor', active: 'textarea' });
    },
  },

  'task checkbox': {
    async run(c) {
      const t = await rendered(c, '- [ ] a');
      await c.pressOn(c.window.locator('#gw-rendered-view input[type=checkbox]'));
      await expect.poll(() => c.gw.getTileContent(t.id), { timeout: 10_000 }).toBe('- [x] a');
    },
  },

  'rendered link': {
    async run(c) {
      await rendered(c, '[away](https://example.com/)');
      const before = (await c.gw.panes()).length;
      await c.pressOn(c.window.locator('#gw-rendered-view a'));
      await expect.poll(async () => (await c.gw.panes()).length, { timeout: 10_000 }).toBe(before + 1);
    },
  },

  terminal: {
    async run(c) {
      const { gw, window } = c;
      await gw.enterSource('home');
      await gw.splitFocusedPaneVertical();
      await gw.waitIdle();
      const f = await gw.focused();
      const cx = Math.round(f.cx);
      const cy = Math.round(f.cy);
      await gw.openMenu();
      await gw.dragCreate('shell', cx, cy);
      await gw.descendCell(cx, cy);
      await gw.shellAttached();
      const other = (await gw.panes()).find((p) => p.id !== f.id)!;
      await gw.focusPane(other);
      expect((await gw.focused()).id).toBe(other.id);
      await c.pressOn(window.locator('.gw-shell-host'));
      await expect.poll(async () => (await gw.focused()).id).toBe(f.id);
      await expect.poll(async () => (await keyboard(window)).owner).toBe('terminal');
      // The tile goes, so its tmux dies before teardown.
      await gw.ascendViaCrumb();
      await expect.poll(async () => (await gw.focused()).textFocus).toBe('');
      await gw.deleteTileCell(cx, cy);
    },
  },

  'circle menu row': {
    async run(c) {
      expect(await c.gw.theme()).toBe('dark');
      const popover = await openCircleMenu(c);
      await c.pressOn(popover.locator('[data-gw-choice="light"]'));
      await expect(popover).toHaveCount(0);
      await expect.poll(() => c.gw.theme()).toBe('light');
    },
  },

  'circle menu outside': {
    async run(c) {
      const popover = await openCircleMenu(c);
      await c.press(await paneMiddle(c));
      await expect(popover).toHaveCount(0);
      expect(await c.gw.theme(), 'nothing was picked').toBe('dark');
    },
  },

  'rename input': {
    async run(c) {
      await doorDescent(c);
      const input = await openRename(c);
      await c.pressOn(input);
      await c.window.waitForTimeout(300);
      await expect(input, 'a press on it keeps it open').toBeVisible();
      expect((await keyboard(c.window)).active).toBe('#gw-rename-input');
      await input.press('Escape');
      await expect(input).toHaveCount(0);
    },
  },

  'rename outside': {
    async run(c) {
      const d = await doorDescent(c);
      const input = await openRename(c);
      await input.fill('renamed by a press');
      await c.press(await paneMiddle(c));
      await expect(input).toHaveCount(0);
      await expect
        .poll(async () => tileAt(await c.gw.getGrid(d.gridID), 'door', d.cx, d.cy)?.altText, { timeout: 10_000 })
        .toBe('renamed by a press');
    },
  },

  'entry input': {
    async run(c) {
      await urlAsking(c);
      await c.pressOn(c.window.locator('#gw-url-input'));
      await expect(c.window.locator('#gw-url-modal.open')).toHaveCount(1);
      expect((await keyboard(c.window)).active).toBe('#gw-url-input');
    },
  },

  'entry cancel': {
    async run(c) {
      const u = await urlAsking(c);
      await c.pressOn(c.window.locator('#gw-url-cancel'));
      await expect(c.window.locator('#gw-url-modal.open')).toHaveCount(0);
      expect(await urlOf(c, u), 'nothing was made').toBe('');
    },
  },

  'entry create': {
    async run(c) {
      const u = await urlAsking(c);
      await c.window.fill('#gw-url-input', 'example.com');
      await c.pressOn(c.window.locator('#gw-url-submit'));
      await expect(c.window.locator('#gw-url-modal.open')).toHaveCount(0);
      await expect.poll(() => urlOf(c, u), { timeout: 10_000 }).toContain('example.com');
    },
  },

  'entry backdrop': {
    async run(c) {
      const u = await urlAsking(c);
      const card = (await c.window.locator('#gw-url-form').boundingBox())!;
      await c.press({ x: card.x + card.width / 2, y: Math.max(2, card.y / 2) });
      await expect(c.window.locator('#gw-url-modal.open')).toHaveCount(0);
      expect(await urlOf(c, u), 'nothing was made').toBe('');
    },
  },

  'entry suggestion': {
    async run(c) {
      const first = await urlAsking(c);
      await c.window.fill('#gw-url-input', 'example.com');
      await c.window.locator('#gw-url-form').evaluate((form: HTMLFormElement) => form.requestSubmit());
      await expect.poll(() => urlOf(c, first), { timeout: 10_000 }).toContain('example.com');
      await expect.poll(async () => (await c.gw.focused()).textFocus).not.toBe('');
      await c.gw.waitIdle();
      await c.gw.ascendViaCrumb();
      await expect.poll(async () => (await c.gw.focused()).textFocus).toBe('');
      // A press while the ascent animates lands its focus alone (gesture.PressStartsGesture).
      await expect.poll(() => c.window.evaluate(() => (window as any).__gridwellTest.transitioning())).toBe(false);
      const second = await urlAsking(c, 1);
      await c.pressOn(c.window.locator('#gw-url-suggest li').first());
      await expect(c.window.locator('#gw-url-modal.open')).toHaveCount(0);
      await expect.poll(() => urlOf(c, second), { timeout: 10_000 }).toContain('example.com');
    },
  },
};

test('every case is a row of the controls table', () => {
  const names = controls().map((r) => r.name);
  expect(Object.keys(cases).filter((k) => !names.includes(k)), 'cases no row names').toEqual([]);
  expect(names.filter((n) => !cases[n]), 'rows with no case').toEqual([]);
});

for (const row of controls()) {
  const k = cases[row.name];
  if (!k) continue;
  test.describe(`control: ${row.name}`, () => {
    if (k.use) test.use(k.use);
    for (const device of ['finger', 'mouse'] as Device[]) {
      test.describe(device, () => {
        test.use({ viewport: VIEWPORT[device] });
        test(`a ${device} press ${row.press}`, async ({ gw, window, serve }) => {
          const press = (at: Pt) =>
            device === 'finger' ? window.touchscreen.tap(at.x, at.y) : window.mouse.click(at.x, at.y);
          const c: Ctx = {
            gw,
            window,
            serve,
            device,
            press: async (at) => {
              await press(at);
              await gw.waitIdle();
            },
            pressRight: async (at) => {
              if (device === 'finger') await longPress(window, at);
              else await window.mouse.click(at.x, at.y, { button: 'right' });
              await gw.waitIdle();
            },
            pressOn: async (el) => {
              await press(await center(el));
              await gw.waitIdle();
            },
          };
          await k.run(c);
          await expect(window.locator('#gw-boot.failed'), 'the client is still running').toHaveCount(0);
          expect((await gw.panes()).length, 'and answers').toBeGreaterThan(0);
        });
      });
    }
  });
}
