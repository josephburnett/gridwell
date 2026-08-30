package server

// Password auth for the browser surface: the mux carrying Connect RPCs,
// static files, and the wasm client. Single-tenant by design — one plaintext
// password, the minted <home>/web-password file named by config.PasswordFile,
// one derived cookie, no accounts, no sessions table.
//
// The one fact is the password, owned by config, and everything else derives
// from it in exactly one place: AuthToken(password) is both the cookie value a
// login sets and the value every request is checked against. Because the check
// is against the current password's token, changing the password invalidates
// every outstanding cookie with no revocation state at all.
//
// Deliberately not gated: the gRPC node export, FederationHandler, which a
// mounter's ssh tunnel dials. Its gate is the kernel — it is served only on
// the 0600 unix socket node.listenFederation opens. Everything on the browser
// door is gated here, the /shell WebSocket included; a PTY is not an
// exception. The desktop app's own window authenticates without prompting: the
// serve banner carries the token and the sidecar pre-sets the cookie.

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
	// browser-enforced maximum, which is the longest modern browsers honor.
	// The cookie is re-issued on every authenticated request, so under regular
	// use the window slides and it never expires. Revocation is rotating the
	// password by deleting the web-password file, never a cookie expiry.
	authCookieMaxAge = 400 * 24 * 60 * 60
)

// AuthToken derives the auth cookie value from the configured password. It is
// the one derivation: the login handler sets it, the middleware checks it, and
// the serve banner prints it for the desktop sidecar. It is stored nowhere, so
// a password change changes the token and thereby signs every browser out.
func AuthToken(password string) string {
	sum := sha256.Sum256([]byte("gridwell-auth-v1\n" + password))
	return hex.EncodeToString(sum[:])
}

// authWrap gates next (the browser mux) behind the configured password.
// There is no open mode: New refuses an empty password, so this is the
// only way onto the mux.
func (s *Server) authWrap(next http.Handler) http.Handler {
	token := AuthToken(s.cfg.Password)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == authLoginPath {
			s.handleLogin(w, r, token)
			return
		}
		if strings.HasPrefix(r.URL.Path, contentPathPrefix) {
			// The /content/ door can never see the cookie: sandboxed pages
			// have opaque origins and the desktop's native views live on
			// their own session partition. It gates itself by the content
			// token in the path; see content_door.go.
			next.ServeHTTP(w, r)
			return
		}
		if authed(r, token) {
			// Re-issue on every authenticated request, so the 400-day cap
			// slides and a regularly-used browser never expires.
			setAuthCookie(w, token)
			next.ServeHTTP(w, r)
			return
		}
		// A browser navigation gets the login page; anything else, an RPC
		// POST or a stream, gets a bare 401 the client surfaces as an
		// error.
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

// handleLogin serves the login page on GET and checks a submitted password on
// POST. The submitted password is compared by token, so the compare is
// constant-time over fixed-length digests. A GET carrying ?token=<token> is
// the token login: opening /auth/login?token=<banner token> sets the cookie
// and lands home without a prompt, which is how a browser reaches a node
// whose password it was never typed. The token is the banner's, at the same
// trust level as the <home>/web-password file the password is read from.
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

// writeLoginPage renders the self-contained login form. It has no static-dir
// dependency, because the static dir is behind the gate this page opens. Only
// fixed strings are interpolated, so nothing here can echo input.
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
