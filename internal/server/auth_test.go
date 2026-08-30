package server

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
)

const spaMarker = "GRIDWELL-SPA-MARKER"

// newAuthTestServer serves the REAL browser surface — WebHandler, exactly
// what `gridwell serve` mounts — with a static dir whose index.html carries a
// marker, so a test can tell the SPA from the login page. The auth tests
// cross this seam on purpose: gating logic verified against anything less
// than the production handler would miss a wiring mistake in WebHandler.
func newAuthTestServer(t *testing.T, password string) (*httptest.Server, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	reg := plugin.NewRegistry()
	_, root := registerPrimaryLocaldb(t, reg, st)

	staticDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"), []byte(spaMarker), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	srv, err := New(reg, Config{StaticFS: os.DirFS(staticDir), Password: password})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.WebHandler())
	t.Cleanup(hs.Close)
	return hs, root
}

// noRedirect returns hs's client with redirects disabled so a 303 is
// observable instead of followed.
func noRedirect(hs *httptest.Server) *http.Client {
	c := *hs.Client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

func get(t *testing.T, c *http.Client, url string, cookie string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: AuthCookieName, Value: cookie})
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res, string(body)
}

// The browser door has no open mode: New
// refuses an empty password. Before this, authWrap had a no-password
// arm that production could never reach (BuildConfig always mints one)
// — dead in the binary, but a standing shortcut for every test that
// mounted the mux without a cookie.
func TestNewRequiresAPassword(t *testing.T) {
	if _, err := New(plugin.NewRegistry(), Config{}); err == nil {
		t.Fatal("New accepted an empty password — the web door must never be open")
	}
}

func TestAuthGatesBrowserSurface(t *testing.T) {
	const pw = "hunter2"
	hs, root := newAuthTestServer(t, pw)
	c := noRedirect(hs)

	// A bare navigation gets the login page, not the SPA.
	res, body := get(t, c, hs.URL+"/", "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET / = %d, want 401", res.StatusCode)
	}
	if strings.Contains(body, spaMarker) || !strings.Contains(body, authLoginPath) {
		t.Fatalf("unauthenticated GET / must serve the login form, not the SPA; got %q", body)
	}

	// Static assets are gated too — the SPA is entirely behind the door.
	if res, _ := get(t, c, hs.URL+"/gridwell.wasm", ""); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated GET /gridwell.wasm = %d, want 401", res.StatusCode)
	}

	// An unauthenticated RPC is a clean 401, not a hang or an HTML page.
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	if _, err := cl.GetGrid(context.Background(), root); err == nil {
		t.Fatalf("unauthenticated RPC must fail")
	}

	// The wrong password re-renders the form and sets no cookie.
	res, err := c.PostForm(hs.URL+authLoginPath, url.Values{"password": {"wrong"}})
	if err != nil {
		t.Fatalf("login POST: %v", err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-password login = %d, want 401", res.StatusCode)
	}
	for _, ck := range res.Cookies() {
		if ck.Name == AuthCookieName {
			t.Fatalf("wrong-password login must not set the auth cookie")
		}
	}

	// The right password sets the derived cookie and redirects home.
	res, err = c.PostForm(hs.URL+authLoginPath, url.Values{"password": {pw}})
	if err != nil {
		t.Fatalf("login POST: %v", err)
	}
	io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/" {
		t.Fatalf("login = %d Location=%q, want 303 to /", res.StatusCode, res.Header.Get("Location"))
	}
	var token string
	for _, ck := range res.Cookies() {
		if ck.Name == AuthCookieName {
			token = ck.Value
		}
	}
	if token != AuthToken(pw) {
		t.Fatalf("login cookie = %q, want AuthToken(password) — the ONE derivation", token)
	}

	// The cookie opens the SPA and every RPC.
	res, body = get(t, c, hs.URL+"/", token)
	if res.StatusCode != http.StatusOK || !strings.Contains(body, spaMarker) {
		t.Fatalf("authed GET / = %d %q, want 200 with the SPA", res.StatusCode, body)
	}
	jar, _ := cookiejar.New(nil)
	u, _ := url.Parse(hs.URL)
	jar.SetCookies(u, []*http.Cookie{{Name: AuthCookieName, Value: token}})
	authedClient := *hs.Client()
	authedClient.Jar = jar
	cl = rpc.NewClient(&authedClient, hs.URL, connect.WithProtoJSON())
	if _, err := cl.GetGrid(context.Background(), root); err != nil {
		t.Fatalf("authed RPC should work: %v", err)
	}

	// A garbage cookie is as good as none.
	if res, _ := get(t, c, hs.URL+"/", "not-the-token"); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("garbage cookie GET / = %d, want 401", res.StatusCode)
	}
}

// TestAuthCookieDiesWithPasswordChange pins the user-facing contract: the
// cookie is checked against the CURRENT password, so changing the password
// (a new serve with a new config) signs every browser out with no stored
// revocation state at all.
func TestAuthCookieDiesWithPasswordChange(t *testing.T) {
	oldCookie := AuthToken("old-password")
	hs, _ := newAuthTestServer(t, "new-password")
	res, _ := get(t, noRedirect(hs), hs.URL+"/", oldCookie)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old-password cookie GET / = %d, want 401 (must re-login)", res.StatusCode)
	}
}

func TestAuthLoginPageDirect(t *testing.T) {
	const pw = "pw"
	hs, _ := newAuthTestServer(t, pw)
	c := noRedirect(hs)

	// Unauthenticated GET of the login path is the form (200 — it IS the
	// right page for that address).
	res, body := get(t, c, hs.URL+authLoginPath, "")
	if res.StatusCode != http.StatusOK || !strings.Contains(body, "password") {
		t.Fatalf("GET %s = %d %q, want the form", authLoginPath, res.StatusCode, body)
	}

	// Authenticated GET bounces home instead of re-prompting.
	res, _ = get(t, c, hs.URL+authLoginPath, AuthToken(pw))
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/" {
		t.Fatalf("authed GET %s = %d, want 303 to /", authLoginPath, res.StatusCode)
	}
}

// The token login: a GET on the login path carrying the
// banner's token sets the cookie and lands home — how a native shell
// that owns a webview's cookie jar (mobile) authenticates without a
// prompt; a wrong token is the login page, 401, no cookie.
func TestTokenLogin(t *testing.T) {
	hs, _ := newAuthTestServer(t, "hunter2")
	client := noRedirect(hs)
	res, _ := get(t, client, TokenLoginURL(hs.URL, "hunter2"), "")
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/" {
		t.Fatalf("token login = %d %q, want 303 to /", res.StatusCode, res.Header.Get("Location"))
	}
	var cookie string
	for _, c := range res.Cookies() {
		if c.Name == AuthCookieName {
			cookie = c.Value
		}
	}
	if cookie != AuthToken("hunter2") {
		t.Fatalf("cookie = %q", cookie)
	}
	res, body := get(t, client, TokenLoginURL(hs.URL, "wrong"), "")
	if res.StatusCode != http.StatusUnauthorized || len(res.Cookies()) != 0 || !strings.Contains(body, "wrong password") {
		t.Fatalf("wrong token = %d cookies=%d", res.StatusCode, len(res.Cookies()))
	}
}
