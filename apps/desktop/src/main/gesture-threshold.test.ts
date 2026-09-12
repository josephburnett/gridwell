import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';
import { VIEW } from './ipc';

// Drift lint for the drag threshold. The same value lives in three places and
// two languages, and if they disagree a right-drag over a live page arms a pane
// gesture on the canvas side while the view side reads a plain right-click.
//
// It cannot be one shared runtime constant: a sandboxed preload may not require
// local modules (urlview-preload.ts), and Go and TypeScript share no source. The
// owner is `dragThreshold` in client/wasm/main.go, and this fails the build if
// either TypeScript copy drifts from it.

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

function literals(path: string, re: RegExp): string[] {
  const src = readFileSync(resolve(repoRoot, path), 'utf8');
  return [...src.matchAll(re)].map((m) => m[1]);
}

test('the drag threshold agrees across the canvas and both native copies', () => {
  const canvas = literal('client/wasm/main.go', /dragThreshold\s*=\s*([\d.]+)/);
  const viewutil = literal('apps/desktop/src/main/viewutil.ts', /RIGHT_DRAG_THRESHOLD\s*=\s*([\d.]+)/);
  const preload = literal('apps/desktop/src/preload/urlview-preload.ts', /RIGHT_DRAG_THRESHOLD\s*=\s*([\d.]+)/);

  assert.equal(viewutil, canvas, 'viewutil.ts RIGHT_DRAG_THRESHOLD drifted from the canvas dragThreshold (the owner)');
  assert.equal(preload, canvas, 'urlview-preload.ts RIGHT_DRAG_THRESHOLD drifted from the canvas dragThreshold (the owner)');
});

// Drift lints for the two right-press thresholds. viewutil.ts owns them, as
// classifyRightPress's defaults, and urlview-preload.ts carries a copy because
// it is sandboxed and cannot import from main. A drifted far threshold means a
// fast flick arms a pane gesture on the canvas but pops a context menu over the
// live view; a drifted time threshold splits the hold-then-move gesture the
// same way.
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

// Drift lint for the view-to-main IPC channel names. VIEW in ipc.ts owns them,
// and the sandboxed preload duplicates them as string literals because it
// cannot import ipc.ts. A rename in ipc.ts compiles clean and the handlers
// never fire: no right-drag gesture, no middle-click ascend, no touch scroll
// over live content, and nothing says why.
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

// Drift lint for the content-zoom chord. The Key* constants in
// client/contentzoom own the set, and viewutil.ts copies it because a live url
// view holds OS keyboard focus and main must recognize the chord before
// forwarding it. A key only Go knows is dead over a live page; one only
// TypeScript knows is forwarded and dropped. Either way the zoom works in one
// focus state and not the other.
test('the zoom chord keys agree between client/contentzoom and viewutil', () => {
  const owner = literals('client/contentzoom/contentzoom.go', /\bKey\w*\s*=\s*"([^"]*)"/g);
  const fn = literalText('apps/desktop/src/main/viewutil.ts', /(export function zoomChordKey[\s\S]*?\n})/);
  const copy = [...fn.matchAll(/case '([^']*)':/g)].map((m) => m[1]);

  assert.ok(owner.length === 4, `expected 4 chord keys in client/contentzoom, got ${JSON.stringify(owner)}`);
  assert.deepEqual(
    [...copy].sort(),
    [...owner].sort(),
    'viewutil.ts zoomChordKey drifted from the Key* constants in client/contentzoom (the owner)',
  );
});
