import { test, expect } from './fixtures';
import { createLeafLink, disableSource, updateText } from './oracle';

// A disabled source is read-only until the node restarts, and that is a
// verdict, not an outage (gwerr.SourceDisabled): a far node that answered once
// and is remembered is switched off with its home on screen. Panning it posts
// a framing write the node refuses, which the client drops without a notice
// and never parks for the backstop to re-post; its text opens with no editor
// and the bar says why, from the node's stamp (Tile.read_only).

test.use({ extraNodes: ['second'] });

test('a disabled far node reads read-only and its writes are dropped, not retried', async ({ gw, window }) => {
  const framingErrors: string[] = [];
  window.on('console', (m) => {
    if (m.type() === 'error' && m.text().includes('SetFraming')) framingErrors.push(m.text());
  });
  await gw.enterPlugin('second');
  const far = (await gw.plugins()).find((p) => p.label === 'second')!;
  const f = await gw.focused();
  const [cx, cy] = [Math.round(f.cx), Math.round(f.cy) - 1];
  // A plain text tile: CreateTile with no link target.
  const note = await createLeafLink(gw.origin, f.gridID, '', 'notes.md', cx, cy);
  await updateText(gw.origin, note.id, Number(note.version ?? 0), 'far words\n');
  await gw.waitClientTileAt(f.id, cx, cy, note.id);
  await gw.waitIdle();
  // Six backstop ticks fit in the quiet window below.
  await window.evaluate(() => (window as any).__gridwellTest.setBackstopMs(500));

  const reads = await gw.watchGridReads();
  await disableSource(gw.origin, far.uuid);
  await expect
    .poll(() => reads.of(f.gridID), { message: 'the view showing the far home reads it again', timeout: 15_000 })
    .toBeGreaterThan(0);
  await reads.stop();
  await gw.waitIdle();

  const posts = () =>
    window.evaluate(() => Number((window as any).__gridwellTest.persistPosts().SetFraming ?? 0));
  const before = await posts();
  await gw.panFocusedGrid(cx + 2, cy + 2, cx + 1, cy + 1);
  await expect.poll(posts, { message: 'the pan posts its framing', timeout: 10_000 }).toBeGreaterThan(before);
  await gw.waitIdle();
  await window.waitForTimeout(3_000);
  expect(await window.evaluate(() => (window as any).__gridwellTest.outbox()), 'nothing parked').toEqual([]);
  const notices = async () =>
    ((await window.evaluate(() => (window as any).__gridwellTest.errors())).notices ?? []) as { source: string }[];
  expect((await notices()).filter((n) => n.source.startsWith('rpc:')), 'no error notice').toEqual([]);
  expect(framingErrors, 'no SetFraming error in the console').toEqual([]);
  expect(await posts(), 'the backstop re-posted nothing').toBe(before + 1);

  // The text the node stamped read-only opens with no editor and says why.
  const g = await gw.focused();
  const tile = (await gw.getGrid(g.gridID)).tiles!.find((t) => t.id === note.id)!;
  await gw.descendCell(Number(tile.x ?? 0), Number(tile.y ?? 0));
  await expect.poll(async () => (await gw.focused()).textFocus).toBe(note.id);
  await expect
    .poll(
      () =>
        window.evaluate(() => {
          const v = document.getElementById('gw-rendered-view');
          return v && v.style.display !== 'none' ? v.textContent ?? '' : '';
        }),
      { timeout: 10_000 },
    )
    .toContain('far words');
  await expect(window.locator('#gw-text-editor'), 'no editor on a disabled source').toBeHidden();
  await expect
    .poll(async () => (await gw.barName()).label, { timeout: 10_000 })
    .toMatch(/read-only: second is disabled until the node restarts/);
  await window.keyboard.type('typed');
  await gw.waitIdle();
  expect(await gw.textareaInfo(), 'typing opened an editor').toBeNull();
  expect(await window.evaluate(() => (window as any).__gridwellTest.outbox()), 'no save parked').toEqual([]);
  expect((await notices()).filter((n) => n.source.includes('WriteContent') || n.source === 'textsave')).toEqual([]);
});
