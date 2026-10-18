// Package servertest stands a real *server.Server up for tests outside
// internal/server. The password is filled in and the web API served with a
// client that already carries the auth cookie, so every test crosses the auth
// seam as a logged-in browser does and none reaches the ungated mux. The
// in-package twin is helpers_test.go.
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

// Serve uses server.WebAPIServer, the API the node runs, and returns a Client
// carrying the auth cookie. Closed at test cleanup.
func Serve(t testing.TB, srv *server.Server) *httptest.Server {
	t.Helper()
	hs := httptest.NewUnstartedServer(nil)
	hs.Config = server.WebAPIServer(srv.WebHandler())
	hs.Start()
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
