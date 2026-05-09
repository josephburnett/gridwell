package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/josephburnett/ascent/internal/rpc"
	"github.com/josephburnett/ascent/internal/store"
)

// newTestServer wires up a Server backed by an in-memory store and a test
// user "alice". Returns the server, the user, and a cookie jar already
// holding alice's session.
func newTestServer(t *testing.T) (*httptest.Server, *store.User, *http.Cookie) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	st.UseFastHashing()
	t.Cleanup(func() { _ = st.Close() })

	u, err := st.CreateUser(context.Background(), "alice", "p")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	srv := New(st, Config{SecureCookie: false})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	// Login via the real endpoint to get a session cookie.
	body := bytes.NewBuffer(nil)
	_ = json.NewEncoder(body).Encode(rpc.LoginRequest{Username: "alice", Password: "p"})
	resp, err := http.Post(hs.URL+"/rpc/Login", "application/json", body)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("login status %d: %s", resp.StatusCode, b)
	}
	resp.Body.Close()
	var sessionCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("no session cookie in login response")
	}
	return hs, u, sessionCookie
}

// callRPC helper: encode req as JSON, POST to /rpc/<method>, decode resp.
func callRPC(t *testing.T, hs *httptest.Server, cookie *http.Cookie, method string, req any, resp any) (int, string) {
	t.Helper()
	var body bytes.Buffer
	if req != nil {
		if err := json.NewEncoder(&body).Encode(req); err != nil {
			t.Fatal(err)
		}
	}
	r, err := http.NewRequest(http.MethodPost, hs.URL+"/rpc/"+method, &body)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	got, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	b, _ := io.ReadAll(got.Body)
	if resp != nil && got.StatusCode == 200 {
		if err := json.Unmarshal(b, resp); err != nil {
			t.Fatalf("decode: %v body=%s", err, b)
		}
	}
	return got.StatusCode, string(b)
}

func TestLoginSetsCookie(t *testing.T) {
	hs, _, cookie := newTestServer(t)
	if cookie.Value == "" {
		t.Fatal("empty cookie value")
	}
	if !cookie.HttpOnly {
		t.Error("cookie should be HttpOnly")
	}
	if cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v", cookie.SameSite)
	}
	_ = hs
}

func TestLoginBadPassword(t *testing.T) {
	hs, _, _ := newTestServer(t)
	body := bytes.NewBuffer(nil)
	_ = json.NewEncoder(body).Encode(rpc.LoginRequest{Username: "alice", Password: "wrong"})
	resp, err := http.Post(hs.URL+"/rpc/Login", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", resp.StatusCode)
	}
}

func TestUnauthenticatedReadIsRejected(t *testing.T) {
	hs, _, _ := newTestServer(t)
	st, body := callRPC(t, hs, nil, "Whoami", &rpc.WhoamiRequest{}, nil)
	if st != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401: %s", st, body)
	}
}

func TestWhoamiAndCreateWell(t *testing.T) {
	hs, u, cookie := newTestServer(t)
	var who rpc.WhoamiResponse
	st, body := callRPC(t, hs, cookie, "Whoami", &rpc.WhoamiRequest{}, &who)
	if st != 200 {
		t.Fatalf("whoami status %d: %s", st, body)
	}
	if who.UserID != u.ID || who.Username != "alice" {
		t.Errorf("whoami = %+v", who)
	}

	var nr rpc.NodeResponse
	st, body = callRPC(t, hs, cookie, "CreateWell", &rpc.CreateWellRequest{
		Path:     rpc.Path{},
		ViewRect: rpc.ViewRect{X: -100, Y: -100, W: 200, H: 200},
		GridID:   u.RootGridID, X: 1, Y: 2, W: 1, H: 1,
	}, &nr)
	if st != 200 {
		t.Fatalf("create well: %d %s", st, body)
	}
	if nr.Node.Type != "well" {
		t.Errorf("got %+v", nr.Node)
	}
}

func TestLocalityRefusedAtRPCLayer(t *testing.T) {
	hs, u, cookie := newTestServer(t)
	st, body := callRPC(t, hs, cookie, "CreateWell", &rpc.CreateWellRequest{
		Path:     rpc.Path{},
		ViewRect: rpc.ViewRect{X: 0, Y: 0, W: 1, H: 1},
		GridID:   u.RootGridID, X: 5, Y: 5, W: 1, H: 1,
	}, nil)
	if st != http.StatusConflict {
		t.Errorf("status = %d, want 409: %s", st, body)
	}
	if !strings.Contains(strings.ToLower(body), "view") && !strings.Contains(strings.ToLower(body), "framed") {
		// Just ensure the error mentions locality somewhere.
		t.Logf("body (informational): %s", body)
	}
}

func TestLogoutClearsCookie(t *testing.T) {
	hs, _, cookie := newTestServer(t)
	r, _ := http.NewRequest(http.MethodPost, hs.URL+"/rpc/Logout", nil)
	r.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// The Set-Cookie header should clear the cookie (MaxAge=-1 ⇒ Expires=epoch).
	found := false
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookieName && c.MaxAge < 0 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected cookie clear: %v", resp.Cookies())
	}
}

func TestSubscribeStreamsEvents(t *testing.T) {
	hs, u, cookie := newTestServer(t)

	// Open Subscribe stream.
	r, _ := http.NewRequest(http.MethodGet, hs.URL+"/rpc/Subscribe", nil)
	r.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("subscribe status %d", resp.StatusCode)
	}

	// Trigger an event: create a well.
	go func() {
		// Small delay so the SSE handler has time to register its
		// subscription before the mutation publishes.
		time.Sleep(50 * time.Millisecond)
		_, _ = callRPCAsync(hs, cookie, "CreateWell", &rpc.CreateWellRequest{
			Path: rpc.Path{}, ViewRect: rpc.ViewRect{X: -10, Y: -10, W: 20, H: 20},
			GridID: u.RootGridID, X: 0, Y: 0, W: 1, H: 1,
		})
	}()

	// Read one event with a timeout via a goroutine.
	buf := make([]byte, 4096)
	doneCh := make(chan int, 1)
	go func() {
		n, _ := resp.Body.Read(buf)
		doneCh <- n
	}()
	select {
	case n := <-doneCh:
		if n == 0 {
			t.Error("got 0 bytes from SSE stream")
		}
		s := string(buf[:n])
		if !strings.HasPrefix(s, "data: ") {
			t.Errorf("expected SSE data line, got %q", s)
		}
	case <-time.After(2 * time.Second):
		t.Error("SSE stream produced no event")
	}
}

// TestSPAFallbackForUnknownPaths verifies that arbitrary client-owned URLs
// (like /g/3/4/5) return index.html so reload doesn't 404. /rpc/* paths
// should still 404 cleanly when the method is unknown.
func TestSPAFallbackForUnknownPaths(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	dir := t.TempDir()
	const indexBody = "<html>index</html>"
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(indexBody), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	const assetBody = "console.log(\"asset\");"
	if err := os.WriteFile(filepath.Join(dir, "wasm_exec.js"), []byte(assetBody), 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}

	srv := New(st, Config{StaticDir: dir, SecureCookie: false})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	tests := []struct {
		path string
		want string
	}{
		{"/", indexBody},
		{"/g/3", indexBody},
		{"/g/27/26/25/24/23/22/21/20/19/16/15/14/12", indexBody},
		{"/wasm_exec.js", assetBody},
	}
	for _, tc := range tests {
		resp, err := http.Get(hs.URL + tc.path)
		if err != nil {
			t.Fatalf("%s: %v", tc.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("%s: status %d, want 200", tc.path, resp.StatusCode)
		}
		if string(body) != tc.want {
			t.Errorf("%s: body = %q, want %q", tc.path, body, tc.want)
		}
	}

	// Unknown /rpc/* method should 404, not fall back to index.
	resp, err := http.Get(hs.URL + "/rpc/Bogus")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("/rpc/Bogus status = %d, want 404", resp.StatusCode)
	}
}

// callRPCAsync is like callRPC but does not run inside a *testing.T (used
// from goroutines).
func callRPCAsync(hs *httptest.Server, cookie *http.Cookie, method string, req any) (int, string) {
	var body bytes.Buffer
	_ = json.NewEncoder(&body).Encode(req)
	r, _ := http.NewRequest(http.MethodPost, hs.URL+"/rpc/"+method, &body)
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
