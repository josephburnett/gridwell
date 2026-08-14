import { test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import * as path from 'node:path';
import { AUTH_COOKIE_NAME, AUTH_COOKIE_MAX_AGE_S } from './authconst';

// Drift-lint across the language seam: the server's auth.go is the owner
// of the cookie name and lifetime; this asserts the TS copies against the
// Go source text so neither side can silently move (the model of
// gesture-threshold.test.ts, which pins the drag threshold the same way).
test('auth cookie constants match the server source', () => {
  const authGo = path.resolve(__dirname, '..', '..', '..', '..', 'internal', 'server', 'auth.go');
  const src = execFileSync('cat', [authGo], { encoding: 'utf8' });
  assert.ok(
    src.includes(`authCookieName = "${AUTH_COOKIE_NAME}"`),
    `server auth.go no longer names the cookie ${AUTH_COOKIE_NAME}`,
  );
  assert.ok(
    src.includes('authCookieMaxAge = 400 * 24 * 60 * 60'),
    'server cookie lifetime changed; update AUTH_COOKIE_MAX_AGE_S',
  );
  assert.equal(AUTH_COOKIE_MAX_AGE_S, 400 * 24 * 60 * 60);
});
