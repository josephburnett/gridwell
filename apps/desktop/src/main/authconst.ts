// The auth-cookie facts the desktop app mirrors from the server
// (internal/server/auth.go). The server owns them; these are forced copies
// for the one place Electron pre-sets the cookie (index.ts), pinned to the
// Go source by authconst.test.ts. A rename on the server side would
// otherwise leave the desktop window prompting for a password.
export const AUTH_COOKIE_NAME = 'gridwell_auth';
export const AUTH_COOKIE_MAX_AGE_S = 400 * 24 * 60 * 60;
