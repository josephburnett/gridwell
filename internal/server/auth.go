package server

// Password auth for the BROWSER surface (the mux: Connect RPCs, static
// files, the wasm client). Single-tenant by design: one plaintext password
// in server.yaml, one derived cookie, no accounts, no sessions table.
//
// The one fact is the password (config-owned); everything else derives from
// it in exactly one place: AuthToken(password) is both the cookie value a
// login sets and the value every request is checked against. Because the
// check is against the CURRENT password's token, changing the password
// invalidates every outstanding cookie with no revocation state at all.
//
// Deliberately NOT gated: the gRPC node export sharing this port (the
// NodeHandler grpc branch) — it carries federation (an ssh mount's tunnel
// dials it) and the Electron shell PTY relay, both established before this
// feature. The transport trust model there is unchanged: bind loopback or a
// VPN-only address (bindWarning says so). The desktop app's own window
// authenticates without prompting: the serve banner carries the token and
// the sidecar pre-sets the cookie (apps/desktop/src/main/sidecar.ts).

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
)

const (
	// AuthCookieName carries the token in browsers. SameSite=Lax keeps
	// cross-site POSTs (all Connect RPCs are POSTs) from riding the cookie.
	AuthCookieName = "gridwell_auth"
	// authLoginPath is the login form's POST target (and the login page's
	// address). Handled by the middleware BEFORE the mux, so it can never
	// collide with the SPA fallback.
	authLoginPath = "/auth/login"
	// authCookieMaxAge is the cookie lifetime in seconds: 400 days, the
	// longest modern browsers honor. The user asked for "no expiration" —
	// this is as close as a cookie gets, and every authenticated request
	// re-issues it, so the window slides forever under any regular use.
	authCookieMaxAge = 400 * 24 * 60 * 60
)

// AuthToken derives the auth cookie value from the configured password. The
// ONE derivation: the login handler sets it, the middleware checks it, and
// the serve banner prints it for the desktop sidecar. Not stored anywhere —
// a password change changes the token and thereby signs every browser out.
func AuthToken(password string) string {
	sum := sha256.Sum256([]byte("gridwell-auth-v1\n" + password))
	return hex.EncodeToString(sum[:])
}

// authWrap gates next (the browser mux) behind the configured password.
// With no password configured it is a no-op — today's open behavior.
func (s *Server) authWrap(next http.Handler) http.Handler {
	if s.cfg.Password == "" {
		return next
	}
	token := AuthToken(s.cfg.Password)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == authLoginPath {
			s.handleLogin(w, r, token)
			return
		}
		if strings.HasPrefix(r.URL.Path, contentPathPrefix) {
			// The /content/ door can never see the cookie (sandboxed pages
			// have opaque origins; the desktop's native views live on their
			// own session partition) — it gates itself by the content token
			// in the path (content_door.go).
			next.ServeHTTP(w, r)
			return
		}
		if authed(r, token) {
			// Re-issue on every authenticated request: the 400-day cap
			// slides, so a regularly-used browser never expires.
			setAuthCookie(w, token)
			next.ServeHTTP(w, r)
			return
		}
		// A browser navigation gets the login page; anything else (RPC
		// POSTs, streams) gets a bare 401 the client surfaces as an error.
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			writeLoginPage(w, http.StatusUnauthorized, false)
			return
		}
		http.Error(w, "gridwell: password required", http.StatusUnauthorized)
	})
}

// authed reports whether the request carries the current token. Hashes are
// fixed-length, so the constant-time compare leaks nothing.
func authed(r *http.Request, token string) bool {
	c, err := r.Cookie(AuthCookieName)
	return err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(token)) == 1
}

func setAuthCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     AuthCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   authCookieMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// handleLogin serves the login page (GET) and checks a submitted password
// (POST). The submitted password is compared by token so the compare is
// constant-time over fixed-length digests.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request, token string) {
	switch r.Method {
	case http.MethodPost:
		candidate := AuthToken(r.FormValue("password"))
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(token)) == 1 {
			setAuthCookie(w, token)
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		writeLoginPage(w, http.StatusUnauthorized, true)
	case http.MethodGet, http.MethodHead:
		if authed(r, token) {
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		writeLoginPage(w, http.StatusOK, false)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// writeLoginPage renders the self-contained login form (no static-dir
// dependency — the static dir is behind the very gate this page opens).
// Only fixed strings are interpolated, so nothing here can echo input.
func writeLoginPage(w http.ResponseWriter, status int, wrong bool) {
	errLine := ""
	if wrong {
		errLine = `<p class="err">wrong password</p>`
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Gridwell</title>
<style>
  body { background:#0c0d11; color:#c8d0d4; font:16px system-ui, sans-serif;
         display:flex; align-items:center; justify-content:center; height:100vh; margin:0 }
  form { display:flex; flex-direction:column; gap:12px; width:min(320px, 80vw) }
  h1 { font-size:18px; font-weight:600; margin:0; color:#7fd4d4 }
  input { background:#14161c; color:#c8d0d4; border:1px solid #2a2e38;
          border-radius:4px; padding:10px 12px; font-size:16px }
  input:focus { outline:none; border-color:#7fd4d4 }
  button { background:#1d4a4a; color:#c8d0d4; border:none; border-radius:4px;
           padding:10px 12px; font-size:16px; cursor:pointer }
  .err { color:#e07070; margin:0; font-size:14px }
</style>
<form method="post" action="` + authLoginPath + `">
  <h1>Gridwell</h1>
  ` + errLine + `
  <input type="password" name="password" placeholder="password" autofocus autocomplete="current-password">
  <button type="submit">enter</button>
</form>`))
}
