package server

// Password auth for the browser surface. Single-tenant by design: one
// plaintext password, the minted <home>/web-password file, one derived cookie,
// no accounts and no sessions table. AuthToken(password) is both the cookie a
// login sets and the value every request is checked against, so changing the
// password invalidates every outstanding cookie with no revocation state.
//
// Deliberately not gated: ConnectionHandler, whose gate is the kernel, since it
// is served only on the 0600 unix socket. Everything on the browser door is
// gated here, the /shell WebSocket included. The desktop app's own window
// authenticates without prompting, from the token in the serve banner.

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
	// authLoginPath is handled by the middleware before the mux, so it can
	// never collide with the SPA fallback.
	authLoginPath = "/auth/login"
	// authCookieMaxAge is 400 days, the browser-enforced maximum. The cookie is
	// re-issued on every authenticated request, so the window slides and it
	// never expires; revocation is deleting the web-password file.
	authCookieMaxAge = 400 * 24 * 60 * 60
)

// AuthToken is the one derivation of the cookie value from the password.
func AuthToken(password string) string {
	sum := sha256.Sum256([]byte("gridwell-auth-v1\n" + password))
	return hex.EncodeToString(sum[:])
}

// authWrap gates the browser mux behind the configured password.
func (s *Server) authWrap(next http.Handler) http.Handler {
	token := AuthToken(s.cfg.Password)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == authLoginPath {
			s.handleLogin(w, r, token)
			return
		}
		if strings.HasPrefix(r.URL.Path, contentPathPrefix) {
			// The /content/ door gates itself; see content_door.go.
			next.ServeHTTP(w, r)
			return
		}
		if authed(r, token) {
			// Re-issue on every authenticated request, so the 400-day cap
			// slides and a regularly-used browser never expires.
			setAuthCookie(w, token)
			if client, stale := s.staleBuild(r); stale {
				s.refuseStaleBuild(w, r, client)
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		// A browser navigation gets the login page; anything else gets a bare
		// 401 the client surfaces as an error.
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			writeLoginPage(w, http.StatusUnauthorized, false)
			return
		}
		http.Error(w, "gridwell: password required", http.StatusUnauthorized)
	})
}

// authed compares fixed-length hashes, so the constant-time compare leaks
// nothing.
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

// handleLogin serves the login page on GET and checks a submitted password on
// POST, by token so the compare is constant-time. A GET carrying ?token= sets
// the cookie and lands home without a prompt, which is how a browser reaches a
// node whose password was never typed; the token is the serve banner's, at the
// same trust level as the web-password file.
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
		if presented := r.URL.Query().Get("token"); presented != "" {
			if subtle.ConstantTimeCompare([]byte(presented), []byte(token)) == 1 {
				setAuthCookie(w, token)
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
			writeLoginPage(w, http.StatusUnauthorized, true)
			return
		}
		writeLoginPage(w, http.StatusOK, false)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// writeLoginPage has no static-dir dependency, because the static dir is behind
// the gate this page opens, and interpolates only fixed strings.
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
