import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join, resolve } from 'node:path';

// Drift lint for the bridge method vocabulary: the eight verbs the renderer
// invokes and the ten listeners it subscribes through. preload.ts owns the
// names; client/wasm spells every one again as a string literal, because Go
// reaches JavaScript by name through js.Value and the two languages share no
// source. A rename on either side compiles clean and the wasm calls a
// non-function at run time — a pane goes live with no view, or a push is never
// heard — with nothing to say why.

const here = dirname(fileURLToPath(import.meta.url)); // apps/desktop/src/main
const repoRoot = resolve(here, '../../../..');
const PRELOAD = 'apps/desktop/src/preload/preload.ts';

function read(path: string): string {
  return readFileSync(resolve(repoRoot, path), 'utf8');
}

function matches(src: string, re: RegExp): string[] {
  return [...src.matchAll(re)].map((m) => m[1]).sort();
}

// Every verb call and listener row, wherever in the shim it sits: bridgeVerb
// is called from more than one file.
function wasmSource(): string {
  const dir = resolve(repoRoot, 'client/wasm');
  return readdirSync(dir)
    .filter((n) => n.endsWith('.go'))
    .map((n) => readFileSync(join(dir, n), 'utf8'))
    .join('\n');
}

test('the wasm calls exactly the bridge verbs the preload exposes', () => {
  const preload = matches(read(PRELOAD), /^ {2}(\w+)\(args: /gm);
  const wasm = matches(wasmSource(), /bridgeVerb\("(\w+)"/g);

  assert.equal(preload.length, 8, `expected 8 verbs in ${PRELOAD}, got ${JSON.stringify(preload)}`);
  assert.deepEqual(
    wasm,
    preload,
    'the bridgeVerb names in client/wasm drifted from the methods preload.ts exposes (the owner); the wasm would call a non-function',
  );
});

test('the wasm subscribes exactly the listeners the preload table declares', () => {
  const table = read(PRELOAD).match(/const LISTENERS = \{([\s\S]*?)\n\} as const;/);
  assert.ok(table, `no LISTENERS table found in ${PRELOAD}`);
  const preload = matches(table![1], /^\s*(\w+):/gm);
  const wasm = matches(wasmSource(), /\{"(\w+)", func\(ev js\.Value\)/g);

  assert.equal(preload.length, 10, `expected 10 listeners in ${PRELOAD}, got ${JSON.stringify(preload)}`);
  assert.deepEqual(
    wasm,
    preload,
    'installWebviewListeners in client/wasm drifted from the LISTENERS table in preload.ts (the owner); the push would never be heard',
  );
});
