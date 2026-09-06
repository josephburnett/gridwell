package server

// The gmail plugin through the whole shipped stack: the binary spawned by the
// production loader, the pluginhost adapter, the router, and the /content/
// door as a browser reaches it.
//
// The seam is what is under test, not the plugin's insides. The plugin
// declares one landing grid — the inbox — and names Starred on the (+) menu;
// the adapter has to turn each declared context into a grid id the node can
// serve, both grids have to list through that mapping, and a message is a TEXT
// tile carrying serves_page whose page the node addresses and the door serves.
//
// And the config crosses this seam too: the node hands the plugin PATHS to the
// user's credential and token, never their contents, plus a private state
// directory. What that has to add up to is only visible from here — the
// spawned binary reads both paths and reaches Gmail with the token they hold,
// and nothing it caches in the state directory carries a secret.
//
// Nothing is injected: the plugin lives in another repository and the
// subprocess is its only door. Gmail is not injected either — testdata/gmail
// holds the JSON shapes Gmail answers with, served over http at the address
// the plugin's `endpoint` config key names, so the real generated Gmail client
// does the real parse. The two repositories share those shapes as data, never
// as a package.

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// gmailNS is the registry key for the gmail plugin in these tests.
const gmailNS = "ug2"

// The secrets the test's token file holds. They are what must never appear in
// the plugin's state directory: it is disposable, and a credential that landed
// there would be deleted with it — or read out of it.
const (
	gmailAccessToken  = "at-not-a-real-access-token"
	gmailRefreshToken = "rt-not-a-real-refresh-token"
	// The client secret in testdata/gmail/credentials.json.
	gmailClientSecret = "NOT-A-REAL-SECRET-example-only"
)

// fakeGmail is the recorded Gmail: the three calls the plugin makes, answered
// from testdata/gmail. It remembers the last Authorization header, which is
// how a test sees that the token file was read and sent.
type fakeGmail struct {
	URL string

	mu   sync.Mutex
	auth string
}

// newFakeGmail starts the recorded Gmail, stopped at the end of the test.
func newFakeGmail(t *testing.T) *fakeGmail {
	t.Helper()
	g := &fakeGmail{}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.auth = r.Header.Get("Authorization")
		g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")

		const base = "/gmail/v1/users/me/messages"
		q := r.URL.Query()
		switch {
		case r.URL.Path == base:
			// Gmail ANDs the label ids, so INBOX+UNREAD is the unread part of
			// the inbox — the listing the plugin's delta walk marks with.
			switch strings.Join(q["labelIds"], "+") {
			case "INBOX":
				w.Write(gmailRecorded(t, "messages-list-inbox.json"))
			case "INBOX+UNREAD":
				w.Write(gmailRecorded(t, "messages-list-inbox-unread.json"))
			case "STARRED":
				w.Write(gmailRecorded(t, "messages-list-starred.json"))
			default: // STARRED+UNREAD
				w.Write(gmailRecorded(t, "messages-list-empty.json"))
			}
		case strings.HasPrefix(r.URL.Path, base+"/"):
			id := strings.TrimPrefix(r.URL.Path, base+"/")
			name := "message-" + id + "-full.json"
			if q.Get("format") == "metadata" {
				name = "message-" + id + "-metadata.json"
			}
			raw, err := os.ReadFile(filepath.Join("testdata", "gmail", name))
			if err != nil {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
				return
			}
			w.Write(raw)
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
		}
	}))
	t.Cleanup(hs.Close)
	g.URL = hs.URL
	return g
}

// Authorization is the last credential the plugin presented.
func (g *fakeGmail) Authorization() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.auth
}

// gmailRecorded reads one of the recorded Gmail answers.
func gmailRecorded(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "gmail", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// gmailTokenFile writes the token file the one-time auth flow would have
// written: an unexpired access token, so nothing in this test reaches Google
// for a refresh.
func gmailTokenFile(t *testing.T) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"access_token":  gmailAccessToken,
		"refresh_token": gmailRefreshToken,
		"token_type":    "Bearer",
		"expiry":        time.Now().Add(time.Hour).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "token.json")
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// gmailStack spawns the plugin against the recorded Gmail and stands it up
// behind the real browser door. stateDir is the private directory the node
// hands the plugin, returned so a test can look at what landed in it.
func gmailStack(t *testing.T) (hs *httptest.Server, cl namespace.Namespace, info *gridwellv1.InfoResponse, g *fakeGmail, stateDir string) {
	t.Helper()
	g = newFakeGmail(t)
	stateDir = t.TempDir()
	// Absolute paths: the plugin is a subprocess and nothing promises it the
	// test's working directory.
	credentials, err := filepath.Abs(filepath.Join("testdata", "gmail", "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Exactly the shape a server.yaml plugins: entry carries — paths, an
	// address, and numbers. No secret is ever a config value.
	cl = newPluginClient(t, "gmail", map[string]string{
		"credentials":  credentials,
		"token":        gmailTokenFile(t),
		"endpoint":     g.URL,
		"state_dir":    stateDir,
		"refresh":      "1h",
		"max_messages": "50",
	})
	reg := plugin.NewRegistry()
	reg.Register(gmailNS, "gmail", cl, nil)
	hs = serveWeb(t, mustNew(t, reg, Config{}))

	info, err = cl.Info(t.Context(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	return hs, cl, info, g, stateDir
}

// The plugin's landing grid is the inbox and Starred rides the (+) menu — and
// both list. The mapping from a declared context to a servable grid id is the
// adapter's, so a plugin unit test cannot see it: it would find Starred
// declared and never learn whether it opens anything.
func TestGmailPluginDeclaresAndListsBothCollections(t *testing.T) {
	_, cl, info, _, _ := gmailStack(t)
	ctx := t.Context()

	if info.RootGridId == "" {
		t.Fatal("the gmail plugin declared no landing grid; its row on the (+) menu would enter nothing")
	}
	if len(info.MenuEntries) != 1 || info.MenuEntries[0].Label != "starred" {
		t.Fatalf("menu entries = %v, want starred alone", info.MenuEntries)
	}
	starred := info.MenuEntries[0]
	// The root is already the plugin's own row on that menu, so no entry may
	// name it again.
	if starred.GridId == "" || starred.GridId == info.RootGridId {
		t.Fatalf("the starred entry opens %q, and the landing grid is %q", starred.GridId, info.RootGridId)
	}

	inbox, err := cl.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: info.RootGridId})
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox.Tiles) != 2 {
		t.Fatalf("the inbox = %v, want its two messages", inbox.Tiles)
	}
	// Every message arrives as a text tile that serves a page. That is the
	// shape the whole client rests on: a url tile's own address wins over
	// serves_page, so a message declared kind "url" would hand the node an
	// address it does not have and the message would never be served.
	for _, tl := range inbox.Tiles {
		if tl.Kind != rpc.KindText || !tl.ServesPage || tl.UrlString != "" {
			t.Errorf("%s = kind %q serves_page %v url %q", tl.AltText, tl.Kind, tl.ServesPage, tl.UrlString)
		}
	}
	lunch := tileWithLabel(t, inbox, "Lunch plans")
	tileWithLabel(t, inbox, "Invoice 41")

	// The starred grid is its own listing, not a copy of the inbox: the one
	// starred message and nothing else.
	star, err := cl.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: starred.GridId})
	if err != nil {
		t.Fatal(err)
	}
	if len(star.Tiles) != 1 {
		t.Fatalf("the starred grid = %v, want the one starred message", star.Tiles)
	}
	tileWithLabel(t, star, "Invoice 41")

	// The second listing — the label intersected with UNREAD — is what marks a
	// message, and it named one of the two.
	if lunch.StatusDetail != "unread" {
		t.Errorf("the unread listing did not mark it: status = %q", lunch.StatusDetail)
	}
}

// Descending into a message opens the email itself, as Gmail's own HTML,
// through the door a browser reaches. Neither side of the seam can check this:
// the plugin answers a key and never sees the URL, and the door addresses a
// tile and never sees the email.
func TestGmailPluginServesAMessageThroughTheContentDoor(t *testing.T) {
	hs, cl, info, _, _ := gmailStack(t)
	inbox, err := cl.GetGrid(t.Context(), &gridwellv1.GetGridRequest{GridId: info.RootGridId})
	if err != nil {
		t.Fatal(err)
	}
	msg := tileWithLabel(t, inbox, "Lunch plans")

	res, body := get(t, hs.Client(), rpc.PageURL(hs.URL, ContentToken(testPassword), gmailNS+"/"+msg.Id), "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET the message = %d %q", res.StatusCode, body)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q", ct)
	}
	if !strings.Contains(body, "<b>friday</b>") {
		t.Errorf("the email did not arrive: %.200q", body)
	}
	// The door's invariant rides an email like any other page.
	if csp := res.Header.Get("Content-Security-Policy"); csp != "sandbox allow-scripts" {
		t.Errorf("CSP = %q", csp)
	}
}

// The node hands the plugin PATHS and a disposable directory, and both halves
// of that have to hold at once: the spawned binary reads the credential and
// token files it was pointed at — proved by Gmail seeing the token those files
// hold — and none of what it caches in the state directory carries a secret.
// Only the seam can check it: the config map and the state directory are the
// node's, the reading and the caching are the plugin's.
func TestGmailPluginReadsItsCredentialPathsAndCachesNoSecret(t *testing.T) {
	_, cl, info, g, stateDir := gmailStack(t)
	if _, err := cl.GetGrid(t.Context(), &gridwellv1.GetGridRequest{GridId: info.RootGridId}); err != nil {
		t.Fatal(err)
	}

	if got, want := g.Authorization(), "Bearer "+gmailAccessToken; got != want {
		t.Errorf("Gmail saw Authorization %q, want %q from the configured token file", got, want)
	}

	files := 0
	err := filepath.WalkDir(stateDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		files++
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, secret := range []string{gmailAccessToken, gmailRefreshToken, gmailClientSecret} {
			if strings.Contains(string(raw), secret) {
				t.Errorf("%s holds a secret; the state directory is disposable and must never be where one lives", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Non-vacuous: the plugin did cache its walk there, and what it cached is
	// what was searched.
	if files == 0 {
		t.Fatal("the state directory is empty; nothing was searched for a secret")
	}
}
