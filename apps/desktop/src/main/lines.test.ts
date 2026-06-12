import { test } from 'node:test';
import assert from 'node:assert/strict';
import { isReadyLine, makeLineSplitter } from './lines';

test('isReadyLine matches the serve banner', () => {
  assert.ok(isReadyLine('gridwell: serving on 127.0.0.1:8099 (db=/x static=./web)'));
  assert.ok(!isReadyLine('gridwell: opening sqlite store at /x/gridwell.db'));
  assert.ok(!isReadyLine('gridwell: orphan cleanup killed 1 stale shell session(s)'));
  assert.ok(!isReadyLine(''));
});

test('makeLineSplitter emits complete lines and buffers partials', () => {
  const got: string[] = [];
  const s = makeLineSplitter((l) => got.push(l));
  s.push('gridwell: serving');
  assert.deepEqual(got, []); // no newline yet → buffered
  s.push(' on 127.0.0.1:8099\nnext line\npar');
  assert.deepEqual(got, ['gridwell: serving on 127.0.0.1:8099', 'next line']);
  s.flush();
  assert.deepEqual(got, ['gridwell: serving on 127.0.0.1:8099', 'next line', 'par']);
});

test('makeLineSplitter strips trailing CR', () => {
  const got: string[] = [];
  const s = makeLineSplitter((l) => got.push(l));
  s.push('windows line\r\nunix line\n');
  assert.deepEqual(got, ['windows line', 'unix line']);
});
