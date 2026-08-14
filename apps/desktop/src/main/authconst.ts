// The auth-cookie facts the desktop app mirrors from the server
// (internal/server/auth.go authCookieName / authCookieMaxAge). The server
// OWNS them; these are forced copies for the one place Electron pre-sets
// the cookie (index.ts) — kept honest by authconst.test.ts the same way
// gesture-threshold.test.ts pins the drag threshold across its copies. A
// silent rename server-side would otherwise make the desktop window start
// prompting for a password.
export const AUTH_COOKIE_NAME = 'gridwell_auth';
export const AUTH_COOKIE_MAX_AGE_S = 400 * 24 * 60 * 60;
