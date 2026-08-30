import { test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import * as path from 'node:path';
import { AUTH_COOKIE_NAME, AUTH_COOKIE_MAX_AGE_S } from './authconst';

// Drift lint across the language seam: the server's auth.go owns the cookie
// name and lifetime, so this asserts the TypeScript copies against the Go
// source text. Neither side can move alone.
test('auth cookie constants match the server source', () => {
  const authGo = path.resolve(__dirname, '..', '..', '..', '..', 'internal', 'server', 'auth.go');
  const src = execFileSync('cat', [authGo], { encoding: 'utf8' });
  assert.ok(
    src.includes(`AuthCookieName = "${AUTH_COOKIE_NAME}"`),
    `server auth.go no longer names the cookie ${AUTH_COOKIE_NAME}`,
  );
  assert.ok(
    src.includes('authCookieMaxAge = 400 * 24 * 60 * 60'),
    'server cookie lifetime changed; update AUTH_COOKIE_MAX_AGE_S',
  );
  assert.equal(AUTH_COOKIE_MAX_AGE_S, 400 * 24 * 60 * 60);
});
