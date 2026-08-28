import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';
import { VIEW } from './ipc';

// Drift-lint for the drag threshold (ARCHITECTURE.md §8 seam #5). The "how far is
// a drag, not a click" threshold is the SAME conceptual value in three places, in
// two languages, and they MUST agree or a gesture is interpreted differently
// depending on where it starts (canvas vs a live URL view): a right-drag over a
// live page could arm a pane gesture on one side while the other still reads a
// right-click, the exact seam-drift the analysis flagged.
//
// It can't be one shared runtime constant: a sandboxed preload may not require
// local modules (urlview-preload.ts), and Go/TS don't share source. So the single
// OWNER is the canvas value, client/wasm/main.go `dragThreshold`, and this lint
// fails the build if either TS copy drifts from it — the same discipline as the
// proto-vs-DDL drift test. Change the owner and this points at every copy to fix.

const here = dirname(fileURLToPath(import.meta.url)); // apps/desktop/src/main
const repoRoot = resolve(here, '../../../..');

function literalText(path: string, re: RegExp): string {
  const src = readFileSync(resolve(repoRoot, path), 'utf8');
  const m = src.match(re);
  assert.ok(m, `no literal found in ${path} (pattern ${re})`);
  return m![1];
}

function literal(path: string, re: RegExp): number {
  return parseFloat(literalText(path, re));
}

test('the drag threshold agrees across the canvas and both native copies', () => {
  const canvas = literal('client/wasm/main.go', /dragThreshold\s*=\s*([\d.]+)/);
  const viewutil = literal('apps/desktop/src/main/viewutil.ts', /RIGHT_DRAG_THRESHOLD\s*=\s*([\d.]+)/);
  const preload = literal('apps/desktop/src/preload/urlview-preload.ts', /RIGHT_DRAG_THRESHOLD\s*=\s*([\d.]+)/);

  assert.equal(viewutil, canvas, 'viewutil.ts RIGHT_DRAG_THRESHOLD drifted from the canvas dragThreshold (the owner)');
  assert.equal(preload, canvas, 'urlview-preload.ts RIGHT_DRAG_THRESHOLD drifted from the canvas dragThreshold (the owner)');
});

// Drift-lint for the right-click time threshold. The "how long must the right
// button be held before a distance-exceeding move becomes a pane gesture" value
// lives in two places: viewutil.ts (exported, unit-tested) and urlview-preload.ts
// (inlined — the preload is sandboxed and cannot import from main). The single
// owner is viewutil.ts; this test fails the build if the preload copy drifts.
// The FAR threshold (#119) joined later and was left out of this lint —
// a drifted far threshold means a fast flick arms a pane gesture on the
// canvas but pops a context menu over the live view.
test('the right-drag far threshold agrees between viewutil and the preload', () => {
  const viewutil = literal('apps/desktop/src/main/viewutil.ts', /RIGHT_DRAG_FAR_THRESHOLD\s*=\s*([\d.]+)/);
  const preload = literal('apps/desktop/src/preload/urlview-preload.ts', /RIGHT_DRAG_FAR_THRESHOLD\s*=\s*([\d.]+)/);

  assert.equal(
    preload,
    viewutil,
    'urlview-preload.ts RIGHT_DRAG_FAR_THRESHOLD drifted from viewutil.ts (the owner); update both and keep them equal',
  );
});

test('the right-drag time threshold agrees between viewutil and the preload', () => {
  const viewutil = literal('apps/desktop/src/main/viewutil.ts', /RIGHT_DRAG_TIME_MS\s*=\s*([\d.]+)/);
  const preload = literal('apps/desktop/src/preload/urlview-preload.ts', /RIGHT_DRAG_TIME_MS\s*=\s*([\d.]+)/);

  assert.equal(
    preload,
    viewutil,
    'urlview-preload.ts RIGHT_DRAG_TIME_MS drifted from viewutil.ts (the owner); update both and keep them equal',
  );
});

// Drift-lint for the view→main IPC channel names. The preload sends on four
// channels (VIEW_RIGHTDOWN, …) that main registers under ipc.ts VIEW.*; the
// preload cannot import ipc.ts (sandboxed — see its header), so the names are
// duplicated as string literals. A rename in ipc.ts compiles clean and the
// handlers simply never fire: no right-drag gesture, no middle-click ascend,
// no touch scroll over live content, and nothing says why. VIEW is the owner.
test('the preload sends on the same VIEW channels ipc.ts declares', () => {
  const preload = 'apps/desktop/src/preload/urlview-preload.ts';
  const copies: Record<keyof typeof VIEW, string> = {
    rightdown: literalText(preload, /VIEW_RIGHTDOWN\s*=\s*'([^']+)'/),
    middledown: literalText(preload, /VIEW_MIDDLEDOWN\s*=\s*'([^']+)'/),
    leftdown: literalText(preload, /VIEW_LEFTDOWN\s*=\s*'([^']+)'/),
    touchscroll: literalText(preload, /VIEW_TOUCHSCROLL\s*=\s*'([^']+)'/),
  };
  for (const key of Object.keys(VIEW) as Array<keyof typeof VIEW>) {
    assert.equal(
      copies[key],
      VIEW[key],
      `urlview-preload.ts VIEW_${key.toUpperCase()} drifted from ipc.ts VIEW.${key} (the owner); the handler would never fire`,
    );
  }
});
