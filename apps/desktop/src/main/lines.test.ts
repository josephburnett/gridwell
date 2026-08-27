import { test } from 'node:test';
import assert from 'node:assert/strict';
import { dialAddr, parseServingLine, windowOrigin, makeLineSplitter } from './lines';

test('parseServingLine extracts the bound address from the serve banner', () => {
  assert.deepEqual(parseServingLine('gridwell: serving on 127.0.0.1:8099 (static=./web plugins=1 federation=/tmp/gw home/federation.sock)'), {
    host: '127.0.0.1',
    port: 8099,
    federation: '/tmp/gw home/federation.sock',
  });
  // A Tailscale bind from server.yaml.
  assert.deepEqual(parseServingLine('gridwell: serving on 100.64.0.7:8080 (static=./web plugins=2 federation=/tmp/gw home/federation.sock)'), {
    host: '100.64.0.7',
    port: 8080,
    federation: '/tmp/gw home/federation.sock',
  });
  // Go announces a wildcard bind as the dual-stack listener address.
  assert.deepEqual(parseServingLine('gridwell: serving on [::]:8080 (static= plugins=1 federation=/tmp/gw home/federation.sock)'), {
    host: '::',
    port: 8080,
    federation: '/tmp/gw home/federation.sock',
  });
  assert.deepEqual(parseServingLine('gridwell: serving on 0.0.0.0:8080 (static= plugins=1 federation=/tmp/gw home/federation.sock)'), {
    host: '0.0.0.0',
    port: 8080,
    federation: '/tmp/gw home/federation.sock',
  });
  assert.deepEqual(parseServingLine('gridwell: serving on [::1]:9000 (static= plugins=1 federation=/tmp/gw home/federation.sock)'), {
    host: '::1',
    port: 9000,
    federation: '/tmp/gw home/federation.sock',
  });
});

test('parseServingLine extracts the auth token when a password is configured', () => {
  const token = 'a'.repeat(64);
  assert.deepEqual(
    parseServingLine(`gridwell: serving on 100.64.0.7:8080 (static=./web plugins=2 auth=${token} federation=/tmp/gw home/federation.sock)`),
    { host: '100.64.0.7', port: 8080, federation: '/tmp/gw home/federation.sock', auth: token },
  );
  // A non-token-shaped auth= is ignored rather than trusted.
  assert.deepEqual(
    parseServingLine('gridwell: serving on 127.0.0.1:8099 (static=./web plugins=1 auth=nope federation=/tmp/gw home/federation.sock)'),
    { host: '127.0.0.1', port: 8099, federation: '/tmp/gw home/federation.sock' },
  );
});

test('parseServingLine marks the "already serving" reprint external', () => {
  // A serve (or `gridwell status`) that found the home's lock held re-emits
  // the RUNNING holder's banner — same fields, external so the app connects
  // instead of treating its exited probe child as the server.
  const token = 'd'.repeat(64);
  assert.deepEqual(
    parseServingLine(`gridwell: already serving on 127.0.0.1:10010 (static=embedded plugins=2 auth=${token} federation=/tmp/gw home/federation.sock)`),
    { host: '127.0.0.1', port: 10010, federation: '/tmp/gw home/federation.sock', auth: token, external: true },
  );
  assert.deepEqual(
    parseServingLine('gridwell: already serving on [::]:8080 (static=embedded plugins=1 federation=/tmp/gw home/federation.sock)'),
    { host: '::', port: 8080, federation: '/tmp/gw home/federation.sock', external: true },
  );
});

test('dialAddr is the federation socket from the banner, whatever the web host', () => {
  // The node export is a unix socket (2026-08-26): the shell relay dials
  // it in grpc-js's unix: form, never the web address — a Tailscale-bound
  // window still reaches its shells locally.
  assert.equal(dialAddr({ host: '0.0.0.0', port: 8080, federation: '/h/.gridwell/federation.sock' }), 'unix:/h/.gridwell/federation.sock');
  assert.equal(dialAddr({ host: '100.64.0.7', port: 8080, federation: '/tmp/gw home/federation.sock' }), 'unix:/tmp/gw home/federation.sock');
});

test('parseServingLine rejects every other line', () => {
  assert.equal(parseServingLine('gridwell: opening sqlite store at /x/gridwell.db'), null);
  assert.equal(parseServingLine('gridwell: orphan cleanup killed 1 stale shell session(s)'), null);
  assert.equal(parseServingLine('gridwell: WARNING: listening on 0.0.0.0:8080 — this is NOT a loopback address.'), null);
  assert.equal(parseServingLine(''), null);
  // A banner-shaped line with a garbage address must not resolve boot.
  assert.equal(parseServingLine('gridwell: serving on nonsense (static= plugins=1 federation=/tmp/gw home/federation.sock)'), null);
  assert.equal(parseServingLine('gridwell: serving on 127.0.0.1:notaport (static= plugins=1 federation=/tmp/gw home/federation.sock)'), null);
  // No federation= is not a serve banner: the shell relay would have
  // nothing to dial (an older binary's banner shape).
  assert.equal(parseServingLine('gridwell: serving on 127.0.0.1:8099 (static=./web plugins=1)'), null);
});

test('windowOrigin maps wildcard hosts to loopback and keeps concrete hosts', () => {
  // Wildcards are reachable locally as loopback.
  assert.equal(windowOrigin({ host: '0.0.0.0', port: 8080, federation: '/tmp/gw home/federation.sock' }), 'http://127.0.0.1:8080');
  assert.equal(windowOrigin({ host: '::', port: 8080, federation: '/tmp/gw home/federation.sock' }), 'http://127.0.0.1:8080');
  assert.equal(windowOrigin({ host: '', port: 8080, federation: '/tmp/gw home/federation.sock' }), 'http://127.0.0.1:8080');
  // A concrete host (e.g. a Tailscale IP) is kept, so the window and a phone
  // share one origin.
  assert.equal(windowOrigin({ host: '100.64.0.7', port: 8080, federation: '/tmp/gw home/federation.sock' }), 'http://100.64.0.7:8080');
  assert.equal(windowOrigin({ host: '127.0.0.1', port: 41000, federation: '/tmp/gw home/federation.sock' }), 'http://127.0.0.1:41000');
  // IPv6 hosts get re-bracketed for the URL.
  assert.equal(windowOrigin({ host: '::1', port: 9000, federation: '/tmp/gw home/federation.sock' }), 'http://[::1]:9000');
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
