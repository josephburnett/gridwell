import { test } from 'node:test';
import assert from 'node:assert/strict';
import { parseServingLine, windowOrigin, makeLineSplitter } from './lines';

test('parseServingLine extracts the bound address from the serve banner', () => {
  assert.deepEqual(parseServingLine('gridwell: serving on 127.0.0.1:8099 (static=./web plugins=1 federation=/tmp/gw home/federation.sock)'), {
    host: '127.0.0.1',
    port: 8099,
  });
  // A Tailscale bind from server.yaml.
  assert.deepEqual(parseServingLine('gridwell: serving on 100.64.0.7:8080 (static=./web plugins=2 federation=/tmp/gw home/federation.sock)'), {
    host: '100.64.0.7',
    port: 8080,
  });
  // Go announces a wildcard bind as the dual-stack listener address.
  assert.deepEqual(parseServingLine('gridwell: serving on [::]:8080 (static= plugins=1 federation=/tmp/gw home/federation.sock)'), {
    host: '::',
    port: 8080,
  });
  assert.deepEqual(parseServingLine('gridwell: serving on 0.0.0.0:8080 (static= plugins=1 federation=/tmp/gw home/federation.sock)'), {
    host: '0.0.0.0',
    port: 8080,
  });
  assert.deepEqual(parseServingLine('gridwell: serving on [::1]:9000 (static= plugins=1 federation=/tmp/gw home/federation.sock)'), {
    host: '::1',
    port: 9000,
  });
});

test('parseServingLine extracts the auth token when a password is configured', () => {
  const token = 'a'.repeat(64);
  assert.deepEqual(
    parseServingLine(`gridwell: serving on 100.64.0.7:8080 (static=./web plugins=2 auth=${token} federation=/tmp/gw home/federation.sock)`),
    { host: '100.64.0.7', port: 8080, auth: token },
  );
  // A non-token-shaped auth= is ignored rather than trusted.
  assert.deepEqual(
    parseServingLine('gridwell: serving on 127.0.0.1:8099 (static=./web plugins=1 auth=nope federation=/tmp/gw home/federation.sock)'),
    { host: '127.0.0.1', port: 8099 },
  );
});

test('parseServingLine marks the "already serving" reprint external', () => {
  // A serve (or `gridwell status`) that found the home's lock held re-emits the
  // running holder's banner: same fields, plus external so the app connects to
  // it instead of treating its own exited probe child as the server.
  const token = 'd'.repeat(64);
  assert.deepEqual(
    parseServingLine(`gridwell: already serving on 127.0.0.1:10010 (static=embedded plugins=2 auth=${token} federation=/tmp/gw home/federation.sock)`),
    { host: '127.0.0.1', port: 10010, auth: token, external: true },
  );
  assert.deepEqual(
    parseServingLine('gridwell: already serving on [::]:8080 (static=embedded plugins=1 federation=/tmp/gw home/federation.sock)'),
    { host: '::', port: 8080, external: true },
  );
});

test('parseServingLine rejects every other line', () => {
  assert.equal(parseServingLine('gridwell: opening sqlite store at /x/gridwell.db'), null);
  assert.equal(parseServingLine('gridwell: orphan cleanup killed 1 stale shell session(s)'), null);
  assert.equal(parseServingLine('gridwell: WARNING: listening on 0.0.0.0:8080 — this is NOT a loopback address.'), null);
  assert.equal(parseServingLine(''), null);
  // A banner-shaped line with a garbage address must not resolve boot.
  assert.equal(parseServingLine('gridwell: serving on nonsense (static= plugins=1 federation=/tmp/gw home/federation.sock)'), null);
  assert.equal(parseServingLine('gridwell: serving on 127.0.0.1:notaport (static= plugins=1 federation=/tmp/gw home/federation.sock)'), null);
  // A banner with no federation= still resolves boot: the desktop app reaches
  // everything it needs over the web door, and a node may serve no federation
  // socket at all.
  assert.deepEqual(parseServingLine('gridwell: serving on 127.0.0.1:8099 (static=./web plugins=1)'), {
    host: '127.0.0.1',
    port: 8099,
  });
});

test('windowOrigin maps wildcard hosts to loopback and keeps concrete hosts', () => {
  // Wildcards are reachable locally as loopback.
  assert.equal(windowOrigin({ host: '0.0.0.0', port: 8080 }), 'http://127.0.0.1:8080');
  assert.equal(windowOrigin({ host: '::', port: 8080 }), 'http://127.0.0.1:8080');
  assert.equal(windowOrigin({ host: '', port: 8080 }), 'http://127.0.0.1:8080');
  // A concrete host (e.g. a Tailscale IP) is kept, so the window and a phone
  // share one origin.
  assert.equal(windowOrigin({ host: '100.64.0.7', port: 8080 }), 'http://100.64.0.7:8080');
  assert.equal(windowOrigin({ host: '127.0.0.1', port: 41000 }), 'http://127.0.0.1:41000');
  // IPv6 hosts get re-bracketed for the URL.
  assert.equal(windowOrigin({ host: '::1', port: 9000 }), 'http://[::1]:9000');
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
