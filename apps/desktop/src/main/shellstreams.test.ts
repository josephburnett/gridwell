// Lifecycle rules of the shell-stream registry (the decisions the WS bridge
// used to make implicitly, now stated and pinned): replace-on-open,
// exactly-once exit, no-ops after close, and late bytes from a replaced
// stream never reaching the renderer as the new stream's output.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { ShellStreams, ShellDialer, ShellStreamHandle, ShellExit } from './shellstreams';

interface FakeStream {
  tileId: string;
  writes: Uint8Array[];
  resizes: Array<{ cols: number; rows: number }>;
  closed: boolean;
  emitData: (d: Uint8Array) => void;
  emitEnd: (message: string, sessionGone: boolean) => void;
}

function harness() {
  const dialed: FakeStream[] = [];
  const dial: ShellDialer = (tileId, _cols, _rows, onData, onEnd): ShellStreamHandle => {
    const fake: FakeStream = {
      tileId,
      writes: [],
      resizes: [],
      closed: false,
      emitData: onData,
      emitEnd: onEnd,
    };
    dialed.push(fake);
    return {
      write: (d) => fake.writes.push(d),
      resize: (cols, rows) => fake.resizes.push({ cols, rows }),
      close: () => {
        fake.closed = true;
      },
    };
  };
  const data: Array<{ paneId: string; bytes: Uint8Array }> = [];
  const exits: ShellExit[] = [];
  const streams = new ShellStreams(
    dial,
    (paneId, bytes) => data.push({ paneId, bytes }),
    (ev) => exits.push(ev),
  );
  return { streams, dialed, data, exits };
}

test('open replaces an existing stream for the pane, closing the old one', () => {
  const h = harness();
  h.streams.open('p1', 'u/1', 80, 24);
  h.streams.open('p1', 'u/2', 80, 24);
  assert.equal(h.dialed.length, 2);
  assert.equal(h.dialed[0].closed, true);
  assert.equal(h.dialed[1].closed, false);
  assert.deepEqual(h.streams.paneIds(), ['p1']);
});

test('late bytes from a replaced stream never reach the renderer', () => {
  const h = harness();
  h.streams.open('p1', 'u/1', 80, 24);
  const old = h.dialed[0];
  h.streams.open('p1', 'u/2', 80, 24);
  old.emitData(new Uint8Array([1, 2, 3])); // straggler from the torn-down PTY
  h.dialed[1].emitData(new Uint8Array([9]));
  assert.equal(h.data.length, 1);
  assert.deepEqual([...h.data[0].bytes], [9]);
});

test('exit fires exactly once, whatever raced', () => {
  const h = harness();
  h.streams.open('p1', 'u/1', 80, 24);
  h.dialed[0].emitEnd('boom', false);
  h.dialed[0].emitEnd('', false); // grpc can fire error then end
  assert.equal(h.exits.length, 1);
  assert.equal(h.exits[0].message, 'boom');
  assert.deepEqual(h.streams.paneIds(), []);
});

test('a local close suppresses the exit report — this side asked', () => {
  const h = harness();
  h.streams.open('p1', 'u/1', 80, 24);
  h.streams.close('p1');
  assert.equal(h.dialed[0].closed, true);
  h.dialed[0].emitEnd('', false); // grpc 'end' still arrives after close
  assert.equal(h.exits.length, 0);
});

test("a replaced stream's late end never freezes the pane's NEW stream", () => {
  const h = harness();
  h.streams.open('p1', 'u/1', 80, 24);
  const old = h.dialed[0];
  h.streams.open('p1', 'u/1', 80, 24); // re-attach (e.g. refresh gesture)
  old.emitEnd('', false); // the torn-down stream's end arrives late
  assert.equal(h.exits.length, 0);
  assert.deepEqual(h.streams.paneIds(), ['p1']);
});

test('write and resize after close are silent no-ops', () => {
  const h = harness();
  h.streams.open('p1', 'u/1', 80, 24);
  h.streams.close('p1');
  h.streams.write('p1', new Uint8Array([1]));
  h.streams.resize('p1', 100, 30);
  assert.equal(h.dialed[0].writes.length, 0);
  assert.equal(h.dialed[0].resizes.length, 0);
});

test('sessionGone rides the exit event', () => {
  const h = harness();
  h.streams.open('p1', 'u/7', 80, 24);
  h.dialed[0].emitEnd('session gone', true);
  assert.equal(h.exits[0].sessionGone, true);
});

test('two panes hold independent streams', () => {
  const h = harness();
  h.streams.open('p1', 'u/1', 80, 24);
  h.streams.open('p2', 'u/2', 80, 24);
  h.streams.write('p2', new Uint8Array([5]));
  assert.equal(h.dialed[0].writes.length, 0);
  assert.equal(h.dialed[1].writes.length, 1);
  h.streams.closeAll();
  assert.equal(h.dialed[0].closed, true);
  assert.equal(h.dialed[1].closed, true);
});
