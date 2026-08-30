import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';
import { VIEW } from './ipc';

// Drift lint for the drag threshold. "How far is a drag, not a click" is the
// same value in three places and two languages, and they must agree or the same
// gesture means different things depending on where it starts: a right-drag over
// a live page could arm a pane gesture on the canvas side while the view side
// still reads a plain right-click.
//
// It cannot be one shared runtime constant. A sandboxed preload may not require
// local modules (urlview-preload.ts), and Go and TypeScript share no source. The
// owner is the canvas value, client/wasm/main.go `dragThreshold`; this lint fails
// the build if either TypeScript copy drifts from it, and points at each copy to
// fix when the owner changes.

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

// Drift lints for the two right-press thresholds. Both live in viewutil.ts
// (classifyRightPress's defaults, unit-tested) and again in urlview-preload.ts,
// which is sandboxed and cannot import from main. viewutil.ts is the owner. A
// drifted far threshold means a fast flick arms a pane gesture on the canvas but
// pops a context menu over the live view; a drifted time threshold splits the
// hold-then-move gesture the same way.
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

// Drift lint for the view→main IPC channel names. The preload sends on four
// channels (VIEW_RIGHTDOWN, …) that main registers under ipc.ts VIEW.*. Being
// sandboxed, the preload cannot import ipc.ts, so the names are duplicated as
// string literals. A rename in ipc.ts compiles clean and the handlers simply
// never fire: no right-drag gesture, no middle-click ascend, no touch scroll
// over live content, and nothing says why. VIEW is the owner.
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
