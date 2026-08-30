// Package servertest stands a real *server.Server up for tests OUTSIDE
// internal/server (pluginhost, parity, the server_test seam tests): the
// password filled in, and the browser door served with a client that
// already carries the auth cookie — so every test crosses the auth seam
// the way a logged-in browser does, and no test can reach the ungated
// mux. The in-package twin is helpers_test.go.
package servertest

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/server"
)

// Password gates every server New builds unless cfg carries its own.
const Password = "test-password"

// New is server.New with Password filled in and the error fatal.
func New(t testing.TB, reg *plugin.Registry, cfg server.Config) *server.Server {
	t.Helper()
	if cfg.Password == "" {
		cfg.Password = Password
	}
	srv, err := server.New(reg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// Serve serves srv's WebHandler (the production browser door) on an
// httptest server whose Client carries the auth cookie for Password.
// Closed at test cleanup.
func Serve(t testing.TB, srv *server.Server) *httptest.Server {
	t.Helper()
	hs := httptest.NewServer(srv.WebHandler())
	t.Cleanup(hs.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(hs.URL)
	jar.SetCookies(u, []*http.Cookie{{Name: server.AuthCookieName, Value: server.AuthToken(Password)}})
	hs.Client().Jar = jar
	return hs
}
