import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { CH, VIEW } from './ipc';

// Main answers a renderer only through registerWebviewIpc's on and handle,
// which ask viewutil.ipcRefusal first; a handler registered any other way
// answers whatever page sends to it.

const here = dirname(fileURLToPath(import.meta.url)); // apps/desktop/src/main

function source(name: string): string {
  return readFileSync(join(here, name), 'utf8');
}

test('no production file registers an ipcMain handler outside the sender check', () => {
  for (const name of readdirSync(here).filter((n) => n.endsWith('.ts') && !n.endsWith('.test.ts'))) {
    const lines = source(name).split('\n');
    lines.forEach((line, i) => {
      if (!/\bipcMain\.(on|once|handle|handleOnce|addListener)\(/.test(line)) return;
      const inWrapper = name === 'register.ts' && /^    ipcMain\.(on|handle)\(channel, \(e, a: A\) => \{$/.test(line);
      assert.ok(inWrapper, `${name}:${i + 1} registers an ipcMain handler outside registerWebviewIpc's on/handle: ${line.trim()}`);
    });
  }
});

test('every canvas verb is handled for the root and every view verb heard from a view, once', () => {
  const src = source('register.ts');
  const handled = [...src.matchAll(/^  handle\(CH\.(\w+),/gm)].map((m) => m[1]).sort();
  const heard = [...src.matchAll(/^  on\(VIEW\.(\w+),/gm)].map((m) => m[1]).sort();
  assert.deepEqual(handled, Object.keys(CH).sort(), 'the CH channels registerWebviewIpc handles');
  assert.deepEqual(heard, Object.keys(VIEW).sort(), 'the VIEW channels registerWebviewIpc hears');
});
