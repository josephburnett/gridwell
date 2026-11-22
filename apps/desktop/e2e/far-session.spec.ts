import * as fs from 'node:fs';
import * as path from 'node:path';
import type { Page } from '@playwright/test';
import { test, expect, homePassword, loginToken } from './fixtures';
import type { GridwellDriver } from './driver';
import { createText, createTile } from './oracle';
import { dumpNow } from './trace';
import { droppedPanes, errorNotices, eventToPaint, maxInFlight, refusedCalls, repeatedReads, roundTrips } from './tracebudget';

// A session of ordinary use on a far node's grids, reached through a door on
// this node's home, run once over a direct hop and once over one that holds
// every chunk far-session.json's delayMs each way (fixtures' delayRelay).
// Every gesture is timed from the input that ends it to the first animation
// frame whose state shows its result. A cell drawn on the gesture stays under
// boundMs whatever the hop; a cell that waits on the far node's answer is
// named in waits, with why, or in decided, with the CLAUDE.md decision that
// keeps it waiting, and the run at the delay must find exactly those
// cells slow, so the list is always exact and its count is the far-waits row
// of test/boundary/ratchet.txt. The session's own dump then stands in for a
// person reading it: no refusal, no read repeated with nothing between, no
// error notice, no pane dropped from a layout, and the reads to a far grid's
// first paint and the saves in flight per text at the numbers recorded, and
// every event folded in painted within boundMs. A failure the session finds
// today is listed under known, with why, so the spec is green and the list,
// counted in the same row, is exact.

const SPEC = path.join(__dirname, 'far-session.json');

interface Recorded {
  delayMs: number;
  boundMs: number;
  waits: Record<string, string>;
  decided: Record<string, string>;
  known: Record<string, string>;
  firstOpenRoundTrips: number;
  textSavesInFlight: number;
}

const recorded = JSON.parse(fs.readFileSync(SPEC, 'utf8')) as Recorded;

// DROPS names the known failure of a workspace that loses a pane.
const DROPS = 'a far workspace keeps its panes';

// Probe is a state the page can be asked about each frame; see probe below.
type Probe =
  | { kind: 'tile'; pane: string; x: number; y: number; want: string } // want '*' any tile, '' none
  | { kind: 'view'; pane: string; was: string }
  | { kind: 'panes'; more: number }
  | { kind: 'bar'; label: string }
  | { kind: 'selected'; pane: string; id: string }
  | { kind: 'checked' }
  | { kind: 'menu' }
  | { kind: 'level'; depth: number }
  | { kind: 'painted'; id: string; text: string };

// msToPaint arms a listener for the input event that ends act, runs act, and
// answers the ms from that event to the first frame on which probe holds,
// or Infinity when it never does.
async function msToPaint(win: Page, on: 'mouseup' | 'mousedown' | 'keydown', probe: Probe, act: () => Promise<void>): Promise<number> {
  await win.evaluate((on) => {
    const w = window as any;
    w.__lat = { t0: 0 };
    window.addEventListener(on, () => (w.__lat.t0 = performance.now()), { capture: true, once: true });
    for (const el of document.querySelectorAll('#gw-rendered-view input[type=checkbox]')) (el as any).__gwOld = true;
  }, on);
  const painted = win.evaluate((p: Probe) => new Promise<number>((resolve, reject) => {
    const w = window as any;
    const h = w.__gridwellTest;
    const holds = (): boolean => {
      switch (p.kind) {
        case 'tile': {
          const at: string = h.tileAt(p.pane, p.x, p.y);
          return p.want === '*' ? at !== '' : at === p.want;
        }
        case 'view': {
          const pn = h.panes().find((q: any) => q.id === p.pane);
          return !!pn && `${pn.gridID} ${pn.cx} ${pn.cy} ${pn.zoom} ${pn.textFocus}` !== p.was;
        }
        case 'panes':
          return h.panes().length >= p.more;
        case 'bar':
          return h.bar().title?.label === p.label;
        case 'selected':
          return (h.selectionPainted()[p.pane] ?? []).includes(p.id);
        case 'checked': {
          const box = document.querySelector('#gw-rendered-view input[type=checkbox]') as any;
          return !!box && !box.__gwOld && box.checked;
        }
        case 'menu':
          return h.menu().open;
        case 'level':
          return h.transitioning() || h.workspace().depth > p.depth;
        case 'painted':
          return (h.paintedText()[p.id] ?? '').includes(p.text);
      }
    };
    const start = performance.now();
    const tick = () => {
      if (w.__lat.t0 > 0 && holds()) return resolve(performance.now() - w.__lat.t0);
      if (performance.now() - start > 10_000) return reject(new Error(`never held: ${JSON.stringify(p)}`));
      requestAnimationFrame(tick);
    };
    tick();
  }), probe).catch((e: Error) => {
    // A probe that never holds is an infinite cell, so one run reports
    // every gesture rather than stopping at the first.
    console.log(`[far-session] ${e.message}`);
    return Infinity;
  });
  await act();
  return painted;
}

async function viewOf(gw: GridwellDriver): Promise<string> {
  const p = await gw.focused();
  return `${p.gridID} ${p.cx} ${p.cy} ${p.zoom} ${p.textFocus}`;
}

interface Session {
  ms: Record<string, number>;
  reads: Record<string, number>;
  firstOpen: { from: number; to: number };
}

async function session(gw: GridwellDriver, win: Page): Promise<Session> {
  const ms: Record<string, number> = {};
  // A read cell is timed to the paint it feeds, which a descent's transition
  // sits in front of whatever the hop, so it is reported and its serial
  // reads pinned rather than held to the bound.
  const reads: Record<string, number> = {};
  const m = win.mouse;

  // This node's home, with a door onto the far home dropped from the + menu.
  await gw.enterSource('home');
  const home = await gw.focused();
  const hx = Math.round(home.cx);
  const hy = Math.round(home.cy);
  await gw.openMenu();
  await gw.dragSourceLink('second', hx, hy);
  await expect.poll(() => win.evaluate(([p, x, y]) => (window as any).__gridwellTest.tileAt(p, x, y), [home.id, hx, hy] as [string, number, number])).not.toBe('');

  let was = await viewOf(gw);
  ms['reach the far home'] = await msToPaint(win, 'mouseup', { kind: 'view', pane: home.id, was }, () => gw.descendCell(hx, hy));
  await gw.waitIdle();
  const f = await gw.focused();
  const pane = f.id;
  // The cells, all inside the pane as it opens: a row of creates at the
  // top, the moved and resized door below them, the far grid's door at the
  // middle. The cell under the pane's center is read off the hook's own
  // cell geometry, so the layout follows whatever zoom the pane opens at.
  const c00 = await win.evaluate(([p]) => (window as any).__gridwellTest.cellCenter(p, 0, 0), [pane] as [string]);
  const c11 = await win.evaluate(([p]) => (window as any).__gridwellTest.cellCenter(p, 1, 1), [pane] as [string]);
  const cx = Math.round((f.x + f.w / 2 - c00.x) / (c11.x - c00.x)) - 3;
  const cy = Math.round((f.y + f.h / 2 - c00.y) / (c11.y - c00.y)) - 2;
  const at = (x: number, y: number): Promise<string> =>
    win.evaluate(([p, x, y]) => (window as any).__gridwellTest.tileAt(p, x, y), [pane, x, y] as [string, number, number]);

  ms['open the far + menu'] = await msToPaint(win, 'mouseup', { kind: 'menu' }, () => gw.openMenu());
  const plus = await gw.menu();
  await m.click(plus.plusX, plus.plusY);
  await gw.waitIdle();

  was = await viewOf(gw);
  const pan = { x: f.x + 30, y: f.y + f.h - 30 };
  ms['pan'] = await msToPaint(win, 'mousedown', { kind: 'view', pane, was }, async () => {
    await m.move(pan.x, pan.y);
    await m.down();
    await m.move(pan.x + 6, pan.y - 6, { steps: 3 });
    await m.up();
  });
  await gw.waitIdle();

  // One of each kind, along a row.
  const kinds = ['door', 'markdown', 'url', 'shell', 'workspace'];
  for (let i = 0; i < kinds.length; i++) {
    await gw.openMenu();
    ms[`create ${kinds[i]}`] = await msToPaint(win, 'mouseup', { kind: 'tile', pane, x: cx + i, y: cy, want: '*' },
      () => gw.dragCreate(kinds[i], cx + i, cy));
    await gw.waitIdle();
  }
  const door = await at(cx, cy);
  const text = await at(cx + 1, cy);

  ms['move'] = await msToPaint(win, 'mouseup', { kind: 'tile', pane, x: cx, y: cy + 1, want: door },
    () => gw.dragTileCell(cx, cy, cx, cy + 1));
  ms['resize'] = await msToPaint(win, 'mouseup', { kind: 'tile', pane, x: cx + 1, y: cy + 2, want: door },
    () => gw.resizeTileCell(cx, cy + 1, cx + 2, cy + 3));

  const tc = await gw.cellCenter(pane, cx + 1, cy);
  ms['select'] = await msToPaint(win, 'mouseup', { kind: 'selected', pane, id: text }, async () => {
    await win.keyboard.down('Shift');
    await m.click(tc.x, tc.y);
    await win.keyboard.up('Shift');
  });
  await gw.waitIdle();
  await win.keyboard.press('Escape');
  await gw.waitIdle();

  ms['copy'] = await msToPaint(win, 'mouseup', { kind: 'tile', pane, x: cx + 5, y: cy, want: '*' },
    () => gw.copyTileCell(cx + 1, cy, cx + 5, cy));
  ms['link'] = await msToPaint(win, 'mouseup', { kind: 'tile', pane, x: cx + 6, y: cy, want: '*' },
    () => gw.linkTileCell(cx + 1, cy, cx + 6, cy));
  ms['delete'] = await msToPaint(win, 'mouseup', { kind: 'tile', pane, x: cx + 6, y: cy, want: '' },
    () => gw.deleteTileCell(cx + 6, cy));

  // Into the door, a rename from the bar, and back out.
  was = await viewOf(gw);
  ms['descend a door'] = await msToPaint(win, 'mouseup', { kind: 'view', pane, was }, () => gw.descendCell(cx, cy + 1));
  await gw.clickBarName('right');
  const input = win.locator('#gw-rename-input');
  await expect(input).toBeVisible();
  await input.fill('far desk');
  ms['rename'] = await msToPaint(win, 'keydown', { kind: 'bar', label: 'far desk' }, () => input.press('Enter'));
  await gw.waitIdle();
  was = await viewOf(gw);
  ms['ascend'] = await msToPaint(win, 'mouseup', { kind: 'view', pane, was }, () => gw.ascendViaCrumb());

  // A text typed, then ticked rendered.
  was = await viewOf(gw);
  ms['descend a text'] = await msToPaint(win, 'mouseup', { kind: 'view', pane, was }, () => gw.descendCell(cx + 1, cy));
  const words = 'the quick brown fox jumps over the lazy dog '.repeat(7);
  await gw.typeText(words);
  await gw.pasteText('\n\n- [ ] tick me\n');
  await gw.toggleTextMode();
  await gw.waitIdle();
  const box = win.locator('#gw-rendered-view input[type=checkbox]').first();
  await expect(box).toBeVisible();
  ms['tick a checkbox'] = await msToPaint(win, 'mouseup', { kind: 'checked' }, () => box.click());
  await gw.toggleTextMode();
  await gw.waitIdle();
  await gw.ascendViaCrumb();

  // A far grid the client has never shown: a door made behind its back,
  // holding a text with a body and a door of its own.
  const inner = await createTile(gw.origin, f.gridID, 'door', cx + 3, cy + 2);
  const marker = 'far-session-body';
  const body = await createText(gw.origin, inner.childGridId!, 0, 0, marker);
  await createTile(gw.origin, inner.childGridId!, 'door', 1, 0);
  await expect.poll(() => at(cx + 3, cy + 2), { timeout: 15_000 }).toBe(inner.id);
  await gw.waitIdle();
  const from = Date.now();
  reads['open a far grid'] = await msToPaint(win, 'mouseup', { kind: 'painted', id: body.id, text: marker },
    () => gw.descendCell(cx + 3, cy + 2));
  const to = Date.now();
  await gw.waitIdle();
  await gw.ascendViaCrumb();

  // The workspace tile made above, entered and left.
  ms['enter a workspace'] = await msToPaint(win, 'mouseup', { kind: 'level', depth: 0 }, () => gw.descendCell(cx + 4, cy));
  await expect.poll(() => win.evaluate(() => (window as any).__gridwellTest.workspace().depth)).toBe(1);
  await gw.leaveWorkspace();
  await gw.waitIdle();

  const n = (await gw.panes()).length;
  ms['split'] = await msToPaint(win, 'mouseup', { kind: 'panes', more: n + 1 }, () => gw.splitFocusedPaneVertical());
  await gw.waitIdle();

  return { ms, reads, firstOpen: { from, to } };
}

// direct is the no-delay run's times, which the delayed run compares against;
// the two run in order in one worker.
let direct: Record<string, number> | null = null;

for (const delay of [0, recorded.delayMs]) {
  test.describe(`@far a session on a far node, ${delay} ms each way on the hop`, () => {
    test.use({ extraNodes: ['second'], farDelayMs: delay });

    test(`ordinary use across a connection, ${delay} ms each way`, async ({ gw, home, window }, info) => {
      test.setTimeout(240_000);
      const token = await loginToken(gw.origin, homePassword(home));
      const s = await session(gw, window);
      const { lines } = await dumpNow(window, gw.origin, token);
      const round = (r: Record<string, number>) => Object.fromEntries(Object.entries(r).map(([k, v]) => [k, Math.round(v)]));
      const trips = roundTrips(lines, s.firstOpen.from, s.firstOpen.to);
      const saves = Math.max(0, ...maxInFlight(lines, 'WriteContent').values());
      const eventPaintMs = Math.round(Math.max(0, ...eventToPaint(lines).map((p) => p.ms)));
      const report = { delay, ms: round(s.ms), reads: round(s.reads), firstOpenRoundTrips: trips, textSavesInFlight: saves, eventPaintMs };
      console.log(`[far-session] ${JSON.stringify(report)}`);
      await info.attach('far-session.json', { body: JSON.stringify(report, null, 2), contentType: 'application/json' });
      await info.attach('dump.jsonl', { body: lines.map((l) => JSON.stringify(l)).join('\n'), contentType: 'application/jsonl' });

      // A wait is what the hop adds, not what a busy machine adds: over the
      // delayed hop a cell is slow when it is over the bound and at least the
      // hop's one-way delay slower than the direct run just measured it.
      if (delay === 0) direct = s.ms;
      const added = (k: string) => (delay > 0 && direct && k in direct ? s.ms[k] - direct[k] : Infinity);
      const slow = Object.keys(s.ms).filter((k) => !(s.ms[k] < recorded.boundMs) && !(added(k) < recorded.delayMs)).sort();
      const named = { ...recorded.waits, ...recorded.decided };
      const listed = delay > 0 ? Object.keys(named).sort() : [];
      expect.soft(slow, `cells at or over ${recorded.boundMs} ms: list a wait in far-session.json with why, or draw it on the gesture; drop a listed cell that is now fast`).toEqual(listed);
      for (const k of Object.keys(named)) expect.soft(s.ms, `listed cell ${k} was not measured`).toHaveProperty([k]);
      expect.soft(Object.values(s.reads).every(Number.isFinite), 'every read cell painted').toBe(true);
      expect.soft(trips, 'serial reads before a far grid first paints').toBe(recorded.firstOpenRoundTrips);
      expect.soft(saves, 'saves of one text in flight at once').toBe(recorded.textSavesInFlight);
      expect.soft(eventPaintMs, `an event folded in is painted within ${recorded.boundMs} ms`).toBeLessThan(recorded.boundMs);

      const fmt = (xs: unknown[]) => JSON.stringify(xs, null, 1);
      expect.soft(fmt(refusedCalls(lines)), 'no call refused').toBe('[]');
      expect.soft(fmt(repeatedReads(lines)), 'no read repeated with nothing between').toBe('[]');
      expect.soft(fmt(errorNotices(lines).map((r) => [r.src, r.msg])), 'no error notice').toBe('[]');
      // A known failure is listed under known with why, so it fails nothing
      // until it is fixed, and fails then until its entry is dropped.
      const dropped = droppedPanes(lines).map((r) => [r.src, r.msg]);
      if (DROPS in recorded.known) expect.soft(dropped.length, `${DROPS} is listed as known but no pane was dropped: drop the entry`).toBeGreaterThan(0);
      else expect.soft(fmt(dropped), 'no pane dropped from a layout').toBe('[]');
    });
  });
}
