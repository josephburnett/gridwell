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

	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/rpc"
	"github.com/josephburnett/gridwell/internal/store"
)

const spaMarker = "GRIDWELL-SPA-MARKER"

// newAuthTestServer serves the REAL browser surface — NodeHandler, exactly
// what `gridwell serve` mounts — with a static dir whose index.html carries a
// marker, so a test can tell the SPA from the login page. The auth tests
// cross this seam on purpose: gating logic verified against anything less
// than the production handler would miss a wiring mistake in NodeHandler.
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
	srv := New(reg, Config{StaticDir: staticDir, Password: password})
	hs := httptest.NewServer(srv.NodeHandler())
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
		req.AddCookie(&http.Cookie{Name: authCookieName, Value: cookie})
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res, string(body)
}

func TestAuthDisabledStaysOpen(t *testing.T) {
	hs, root := newAuthTestServer(t, "")
	res, body := get(t, hs.Client(), hs.URL+"/", "")
	if res.StatusCode != http.StatusOK || !strings.Contains(body, spaMarker) {
		t.Fatalf("no-password GET / = %d %q, want 200 with the SPA", res.StatusCode, body)
	}
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())
	if _, err := cl.GetGrid(context.Background(), root); err != nil {
		t.Fatalf("no-password RPC should work: %v", err)
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
		if ck.Name == authCookieName {
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
		if ck.Name == authCookieName {
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
	jar.SetCookies(u, []*http.Cookie{{Name: authCookieName, Value: token}})
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
