import { test, expect } from './fixtures';
import { placeTile, tileAt, updateText } from './oracle';

// Things stay as you left them, whoever left them. Another window moves or
// edits a tile while this window is mid-drag on it: the drop still claims the
// tile as the press saw it, so the node refuses it, and the tile stays where
// the other window put it, as it was edited, on disk and on screen.

test('a stale move and a stale delete are refused, and the tile stays as the other window left it', async ({ gw, window }) => {
  await gw.enterPlugin('home');
  const f = await gw.focused();
  const cx = Math.round(f.cx) - 2;
  const cy = Math.round(f.cy) - 1;
  await gw.openPalette();
  await gw.dragCreate('markdown', cx, cy);
  const tile = tileAt(await gw.getGrid(f.gridID), 'text', cx, cy)!;
  expect(tile, 'the tile persisted').toBeTruthy();
  await gw.waitClientTileAt(f.id, cx, cy, tile.id);
  await gw.waitIdle();
  const m = window.mouse;

  // A move: the press sees the tile at (cx, cy); another window moves it
  // three cells right before the drop.
  const from = await gw.cellCenter(f.id, cx, cy);
  const to = await gw.cellCenter(f.id, cx, cy + 2);
  await m.move(from.x, from.y);
  await m.down({ button: 'left' });
  await m.move(from.x + 8, from.y + 8);
  await placeTile(gw.origin, tile.id, undefined, f.gridID, cx + 3, cy, Number(tile.w ?? 1), Number(tile.h ?? 1));
  await m.move(to.x, to.y, { steps: 8 });
  await m.up({ button: 'left' });
  await gw.waitIdle();
  await gw.waitClientTileAt(f.id, cx + 3, cy, tile.id);
  expect(tileAt(await gw.getGrid(f.gridID), 'text', cx + 3, cy)?.id, "the other window's move stands").toBe(tile.id);

  // A delete: the press sees the tile's words; another window edits them
  // before the drop on the trashcan.
  const here = await gw.cellCenter(f.id, cx + 3, cy);
  const pal = await gw.palette();
  await m.move(here.x, here.y);
  await m.down({ button: 'left' });
  await m.move(here.x + 8, here.y + 8);
  const row = tileAt(await gw.getGrid(f.gridID), 'text', cx + 3, cy)!;
  await updateText(gw.origin, tile.id, Number(row.version ?? 0), '# edited elsewhere\n');
  await m.move(pal.plusX, pal.plusY, { steps: 8 });
  await m.up({ button: 'left' });
  await gw.waitIdle();
  await gw.waitClientTileAt(f.id, cx + 3, cy, tile.id);
  const kept = tileAt(await gw.getGrid(f.gridID), 'text', cx + 3, cy);
  expect(kept?.id, 'the edited tile was not deleted').toBe(tile.id);
  expect(await gw.getTileContent(tile.id)).toBe('# edited elsewhere\n');
});
