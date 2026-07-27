import { test, expect, GridwellDriver } from './fixtures';
import { tileAt } from './oracle';

// A rendered-mode edit typed just before an embed jump must still reach the
// server. Before the content-ownership refactor the pending-edit fact lived
// in THREE places (the cache entry's dirty bit, the App's textareaDirty, and
// the pane-local Dirty), and the rendered-mode save trigger read the
// PANE-scoped one — which the embed descent's onComplete reset for the pane.
// The edit stayed in the cache (rendering as if saved) but no trigger ever
// posted it: client-only state, gone on reload (charter §7).

async function embedHits(window: any): Promise<Array<{ x: number; y: number; w: number; h: number; tileId: string }>> {
  return window.evaluate(() => (window as any).__gridwellTest.embedHits());
}

test('a rendered-mode edit typed right before an embed jump still persists', async ({ gw, window }) => {
  await gw.enterPlugin('localdb');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy) - 1;

  // A target tile and a doc that embeds it.
  await gw.openPalette();
  await gw.dragCreate('markdown', cx + 1, cy);
  const target = tileAt(await gw.getGrid(f.gridID), 'text', cx + 1, cy)!;
  await gw.descendCell(cx + 1, cy);
  await gw.typeText('# the target');
  await gw.rightClickPlus();
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const doc = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;

  await gw.descendCell(cx, cy);
  await gw.typeText(`intro [box](/${target.id})`);
  await gw.toggleTextMode(); // rendered
  await gw.waitIdle();

  // Type the edit and click the embed INSIDE the save-debounce window: the
  // keystrokes are in (rendered edits update the content store immediately),
  // but no save has fired yet when the pane descends away. No waitIdle
  // between — the whole point is to leave with the save still pending.
  await window.keyboard.type(' EDIT');
  const hits = await embedHits(window);
  expect(hits.length, 'the embed rendered').toBe(1);
  await window.mouse.click(hits[0].x + hits[0].w / 2, hits[0].y + hits[0].h / 2);
  await gw.waitIdle();
  expect((await gw.focused()).textFocus, 'the embed click descended').toBe(target.id);

  // The doc's edit must reach the server anyway — the pending edit belongs
  // to the DOC (tile-scoped), not to whichever tile the pane shows now.
  await expect
    .poll(async () => gw.getTileContent(doc.id), { timeout: 10_000 })
    .toContain('EDIT');
});

test('a text-mode edit survives focus-away to a plain grid pane (issue #155)', async ({ gw, window }) => {
  // The pre-rework debounce timer DECLINED to save when focus had moved
  // away, delegating to a rebind flush that never ran when the newly
  // focused pane was a plain grid pane — the last edit window vanished
  // with no notice (issue #155). Since the 2026-07-18 content-ownership
  // rework the timer sweeps every dirty tile-keyed entry regardless of
  // focus; this spec pins that: type within the debounce window, click a
  // sibling GRID pane (no text descent anywhere), and the edit must still
  // reach the server.
  await gw.enterPlugin('localdb');
  const f = await gw.focused();
  const cx = Math.round(f.cx);
  const cy = Math.round(f.cy);
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const doc = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;

  // Pane A stays a grid pane; pane B descends into the doc and types.
  await gw.splitFocusedPaneVertical();
  await gw.waitIdle();
  const panes = await gw.panes();
  const paneA = panes.find((p) => !p.focused)!;
  await gw.descendCell(cx, cy);
  await gw.typeText('first line saved');
  await gw.waitIdle();

  // Type INSIDE the debounce window, then immediately focus the grid pane —
  // no waitIdle between, the save must still be pending when focus leaves.
  await window.keyboard.type(' STRANDED?');
  await gw.focusPane(paneA);

  await expect
    .poll(async () => gw.getTileContent(doc.id), { timeout: 10_000 })
    .toContain('STRANDED?');
});
