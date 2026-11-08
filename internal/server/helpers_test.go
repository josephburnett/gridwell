package server

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
)

// testPassword is the password every in-package test server is gated behind
// unless a test configures its own. A password is required, and New refuses an
// empty one, so there is no ungated shortcut for tests to lean on.
const testPassword = "test-password"

// mustNew is New with the test password filled in and the error
// fatal. servertest.New is its twin for tests outside this package.
func mustNew(t testing.TB, src *Sources, cfg Config) *Server {
	t.Helper()
	if cfg.Password == "" {
		cfg.Password = testPassword
	}
	srv, err := New(*src, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// serveWeb serves the REAL web API — WebHandler, exactly what
// `gridwell serve` mounts — and seeds the httptest client's cookie jar
// with the auth cookie, so every request through hs.Client() crosses the
// auth seam as a logged-in browser would. There is deliberately no way
// to serve the bare mux: a test that could bypass authWrap would not
// notice a wiring mistake in it.
func serveWeb(t testing.TB, srv *Server) *httptest.Server {
	t.Helper()
	return withCookie(t, webAPITest(t, srv.WebHandler()), srv.cfg.Password)
}

// webAPITest serves h on an httptest server whose config is the production
// web API shape (WebAPIServer), started and closed at cleanup. An in-package
// harness crosses the server the node runs, not httptest's default one — the
// copy that diverged on the connection API and must not on this one.
func webAPITest(t testing.TB, h http.Handler) *httptest.Server {
	t.Helper()
	hs := httptest.NewUnstartedServer(nil)
	hs.Config = WebAPIServer(h)
	hs.Start()
	t.Cleanup(hs.Close)
	return hs
}

// withCookie seeds hs's client with the auth cookie for password.
// (servertest.withCookie is the same eight lines for external tests —
// an in-package test cannot import servertest without a cycle.)
func withCookie(t testing.TB, hs *httptest.Server, password string) *httptest.Server {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(hs.URL)
	jar.SetCookies(u, []*http.Cookie{{Name: AuthCookieName, Value: AuthToken(password)}})
	hs.Client().Jar = jar
	return hs
}

// oneSwitch is the switch over one declared connection, for a test that
// disables it with no transport behind it.
type oneSwitch struct {
	name string
	off  bool
}

func (s *oneSwitch) Disable(name string) error {
	if name != s.name {
		return fmt.Errorf("%q is not a connection this node declares", name)
	}
	s.off = true
	return nil
}

func (s *oneSwitch) DisabledReason(name string) string {
	if s.off && name == s.name {
		return rpc.DisabledReason(name)
	}
	return ""
}

func (s *oneSwitch) DisabledNow() []string {
	if s.off {
		return []string{s.name}
	}
	return nil
}
