import { test } from 'node:test';
import assert from 'node:assert/strict';
import { partitionFor, roundBounds, boundsEqual } from './viewutil';

test('partitionFor is persistent and keyed by objectId', () => {
  assert.equal(partitionFor('abc-123'), 'persist:tile-abc-123');
  // Same tile → same partition (shared cookie jar across panes/time).
  assert.equal(partitionFor('abc-123'), partitionFor('abc-123'));
  // Different tiles → different partitions (isolation).
  assert.notEqual(partitionFor('abc-123'), partitionFor('def-456'));
});

test('partitionFor sanitizes stray characters', () => {
  assert.equal(partitionFor('a/b c:d'), 'persist:tile-abcd');
});

test('roundBounds snaps to ints and floors size at 1', () => {
  assert.deepEqual(roundBounds({ x: 10.4, y: 20.6, width: 100.2, height: 50.9 }), {
    x: 10,
    y: 21,
    width: 100,
    height: 51,
  });
  assert.deepEqual(roundBounds({ x: 0, y: 0, width: 0, height: 0 }), {
    x: 0,
    y: 0,
    width: 1,
    height: 1,
  });
});

test('boundsEqual compares all four fields', () => {
  const a = { x: 1, y: 2, width: 3, height: 4 };
  assert.ok(boundsEqual(a, { ...a }));
  assert.ok(!boundsEqual(a, { ...a, x: 9 }));
  assert.ok(!boundsEqual(a, { ...a, height: 9 }));
});
