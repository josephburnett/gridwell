package server

// The gmail plugin through the whole shipped stack: the binary spawned by the
// production loader, the pluginhost adapter, the router, and the /content/
// door as a browser reaches it.
//
// What the seam has to hold: the adapter turns each declared context into a
// grid id the node can serve, every grid lists through that mapping, a
// message is one url tile in all mail carrying serves_page whose page the
// door serves, and a label's row is a link to that tile. The
// config crosses it too — the node hands over paths to the user's credential
// and token, never their contents, plus a private state directory, and nothing
// cached there carries a secret.
//
// Nothing is injected. The plugin lives in another repository, so the
// subprocess is its only door, and testdata/gmail holds the JSON shapes Gmail
// answers with, served at the address the `endpoint` config key names, so the
// real generated client does the real parse.

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/plugintest"
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

// fakeGmail is the recorded Gmail: the five calls the plugin makes, answered
// from testdata/gmail. It remembers the last Authorization header, which is
// how a test sees that the token file was read and sent, and counts the label
// listings and metadata reads, which is how a test tells a full walk from a
// history catch-up. history.list answers one recorded delta since the
// profile's history id and nothing since the id that delta ends at; while held
// it answers nothing since whatever id it is asked from, which is how a test
// chooses when the new mail arrives.
type fakeGmail struct {
	URL string

	mu       sync.Mutex
	auth     string
	lists    int
	metadata []string
	history  int
	held     bool
	refused  bool // every request is answered as Google answers a revoked token
}

// newFakeGmail starts the recorded Gmail, stopped at the end of the test.
func newFakeGmail(t *testing.T) *fakeGmail {
	t.Helper()
	g := &fakeGmail{}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.auth = r.Header.Get("Authorization")
		refused := g.refused
		g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		if refused {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"code":401,"message":"Request had invalid authentication credentials.","status":"UNAUTHENTICATED"}}`))
			return
		}

		const me = "/gmail/v1/users/me"
		const base = me + "/messages"
		q := r.URL.Query()
		switch {
		case r.URL.Path == me+"/profile":
			w.Write(gmailRecorded(t, "profile.json"))
		case r.URL.Path == me+"/history":
			g.mu.Lock()
			g.history++
			held := g.held
			g.mu.Unlock()
			switch {
			case held:
				// Gmail's answer when nothing happened: the id asked from.
				fmt.Fprintf(w, `{"historyId": %q}`, q.Get("startHistoryId"))
			case q.Get("startHistoryId") == "9912345":
				w.Write(gmailRecorded(t, "history-list-new-mail.json"))
			default:
				w.Write(gmailRecorded(t, "history-list-quiet.json"))
			}
		case r.URL.Path == base:
			g.mu.Lock()
			g.lists++
			g.mu.Unlock()
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
				g.mu.Lock()
				g.metadata = append(g.metadata, id)
				g.mu.Unlock()
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

// reads is what the plugin has asked so far: label listings, the ids read for
// metadata, and history requests.
func (g *fakeGmail) reads() (lists int, metadata []string, history int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lists, append([]string(nil), g.metadata...), g.history
}

// refuse answers every request as Google answers a revoked token, until it
// is called with false.
func (g *fakeGmail) refuse(refused bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.refused = refused
}

// holdHistory keeps the recorded delta back until it is called with false.
func (g *fakeGmail) holdHistory(held bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.held = held
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

// gmailConfig is exactly the shape a server.yaml plugins: entry carries for
// the plugin over g — paths, an address, and numbers, never a secret — with
// the private state directory it names.
func gmailConfig(t *testing.T, g *fakeGmail, refresh string) (cfg map[string]string, stateDir string) {
	t.Helper()
	stateDir = t.TempDir()
	// Absolute: the plugin is a subprocess and nothing promises it the test's
	// working directory.
	credentials, err := filepath.Abs(filepath.Join("testdata", "gmail", "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"credentials":  credentials,
		"token":        gmailTokenFile(t),
		"endpoint":     g.URL,
		"state_dir":    stateDir,
		"refresh":      refresh,
		"max_messages": "50",
	}, stateDir
}

// gmailStack spawns the plugin against the recorded Gmail and stands it up
// behind the real browser door, refreshing no more often than refresh.
// stateDir is the private directory the node hands the plugin, returned so a
// test can look at what landed in it.
func gmailStack(t *testing.T, refresh string) (hs *httptest.Server, cl namespace.Namespace, info *gridwellv1.InfoResponse, g *fakeGmail, stateDir string) {
	t.Helper()
	g = newFakeGmail(t)
	var cfg map[string]string
	cfg, stateDir = gmailConfig(t, g, refresh)
	cl = newPluginClient(t, "gmail", cfg)
	reg := plugin.NewRegistry()
	reg.Register(gmailNS, "gmail", cl, nil)
	hs = serveWeb(t, mustNew(t, reg, Config{}))

	info, err := cl.Info(t.Context(), &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	return hs, cl, info, g, stateDir
}

// The plugin's contexts are three (+) menu entries — no landing grid, no
// privileged one — and each lists: all mail holds each message once, and a
// label holds a link to it. The mapping from a declared context to a servable
// grid id, and from a link_target to a tile id, are the adapter's, so a plugin
// unit test cannot see them: it would find the entries declared and never
// learn whether a link resolves to the tile all mail lists.
func TestGmailPluginDeclaresAndListsEveryContext(t *testing.T) {
	_, cl, info, _, _ := gmailStack(t, "1h")
	ctx := t.Context()

	if info.RootGridId != "" {
		t.Fatalf("a plugin is not a place; it named a grid of its own: %q", info.RootGridId)
	}
	if len(info.MenuEntries) != 3 || info.MenuEntries[0].Label != "inbox" ||
		info.MenuEntries[1].Label != "starred" || info.MenuEntries[2].Label != "all mail" {
		t.Fatalf("menu entries = %v, want inbox, starred and all mail", info.MenuEntries)
	}
	grids := map[string]bool{}
	for _, m := range info.MenuEntries {
		grids[m.GridId] = true
	}
	if len(grids) != 3 || grids[""] {
		t.Fatalf("the three contexts must open three grids: %v", info.MenuEntries)
	}

	all, err := cl.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: info.MenuEntries[2].GridId})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Tiles) != 2 {
		t.Fatalf("all mail = %v, want each of the two messages once", all.Tiles)
	}
	// Every message arrives as a url tile whose page the plugin serves. That
	// is the shape the whole client rests on: the address is the node's to
	// derive at its /content/ door, so the message declares none of its own,
	// and a text tile could not serve one at all.
	for _, tl := range all.Tiles {
		if tl.Kind != rpc.KindURL || !tl.ServesPage || tl.UrlString != "" || tl.Reference {
			t.Errorf("%s = kind %q serves_page %v url %q reference %v", tl.AltText, tl.Kind, tl.ServesPage, tl.UrlString, tl.Reference)
		}
	}

	// A tile is named by its subject, and its state is one emoji the client
	// draws beside the name, only when there is something to notice: Lunch
	// plans is unread, Invoice 41 is read and starred, which the starred
	// grid already says.
	status := map[string]map[string]string{
		"all mail": {"Lunch plans": "●", "Invoice 41": "★"},
		"inbox":    {"Lunch plans": "●", "Invoice 41": "★"},
		"starred":  {"Invoice 41": ""},
	}
	for subject, want := range status["all mail"] {
		if tl := tileWithLabel(t, all, subject); tl.AltText != subject || tl.StatusDetail != want {
			t.Errorf("all mail's %q = label %q status %q, want status %q", subject, tl.AltText, tl.StatusDetail, want)
		}
	}

	// A label lists links, each to the tile all mail lists for the message:
	// the inbox's two, and the starred grid's one, not a copy of the inbox.
	for i, want := range [][]string{{"Lunch plans", "Invoice 41"}, {"Invoice 41"}} {
		label, err := cl.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: info.MenuEntries[i].GridId})
		if err != nil {
			t.Fatal(err)
		}
		if len(label.Tiles) != len(want) {
			t.Fatalf("%s = %v, want %v", info.MenuEntries[i].Label, label.Tiles, want)
		}
		for _, subject := range want {
			link := tileWithLabel(t, label, subject)
			if link.LinkTargetId != tileWithLabel(t, all, subject).Id {
				t.Errorf("%s's %q links to %q, want all mail's tile", info.MenuEntries[i].Label, subject, link.LinkTargetId)
			}
			if want := status[info.MenuEntries[i].Label][subject]; link.AltText != subject || link.StatusDetail != want {
				t.Errorf("%s's %q = label %q status %q, want status %q", info.MenuEntries[i].Label, subject, link.AltText, link.StatusDetail, want)
			}
		}
	}
}

// A token Google refuses is a config the plugin cannot serve: the spawned
// binary refuses Info with a sentence naming the command that fixes it, and
// passes once Google accepts the token, with no restart. The refusal is the
// plugin's reading of Google's 401 and the sentence is what the node shows,
// so only the real binary over a recorded Gmail proves both.
func TestGmailRefusesATokenGoogleRefuses(t *testing.T) {
	g := newFakeGmail(t)
	g.refuse(true)
	cfg, _ := gmailConfig(t, g, "1h")
	cp := plugintest.Spawn(t, "gmail", cfg)

	_, err := cp.Info(t.Context(), &pluginv1.InfoRequest{})
	if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "gridwell-plugin-gmail -auth -credentials") {
		t.Fatalf("Info with a refused token = %v, want the refusal naming the auth command", err)
	}
	g.refuse(false)
	if _, err := cp.Info(t.Context(), &pluginv1.InfoRequest{}); err != nil {
		t.Fatalf("Info once Google accepts the token = %v", err)
	}
}

// Descending into a message opens the email itself, as Gmail's own HTML,
// through the door a browser reaches. Neither side of the seam can check this:
// the plugin answers a key and never sees the URL, and the door addresses a
// tile and never sees the email.
func TestGmailPluginServesAMessageThroughTheContentDoor(t *testing.T) {
	hs, cl, info, _, _ := gmailStack(t, "1h")
	inbox, err := cl.GetGrid(t.Context(), &gridwellv1.GetGridRequest{GridId: info.MenuEntries[0].GridId})
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
	_, cl, info, g, stateDir := gmailStack(t, "1h")
	if _, err := cl.GetGrid(t.Context(), &gridwellv1.GetGridRequest{GridId: info.MenuEntries[0].GridId}); err != nil {
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

// After the first refresh walks every collection, the next one reads Gmail's
// history since the id the walk was current to and applies exactly what it
// names: the new inbox message is read and listed, the message that arrived
// with no watched label is not read, and no collection is listed again. The
// history id is the plugin's and the listing the node's, so only the seam sees
// the catch-up reach a grid.
func TestGmailPluginCatchesUpFromHistoryWithoutAWalk(t *testing.T) {
	_, cl, info, g, _ := gmailStack(t, "200ms")
	inboxID := info.MenuEntries[0].GridId
	inbox, err := cl.GetGrid(t.Context(), &gridwellv1.GetGridRequest{GridId: inboxID})
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox.Tiles) != 2 {
		t.Fatalf("the walked inbox = %v, want its two messages", inbox.Tiles)
	}
	walkLists, walkMeta, _ := g.reads()
	if walkLists == 0 {
		t.Fatal("the first refresh listed nothing; it was not a walk")
	}

	deadline := time.Now().Add(15 * time.Second)
	for len(inbox.Tiles) != 3 {
		if time.Now().After(deadline) {
			t.Fatalf("the history delta never reached the inbox: %v", inbox.Tiles)
		}
		time.Sleep(100 * time.Millisecond)
		if inbox, err = cl.GetGrid(t.Context(), &gridwellv1.GetGridRequest{GridId: inboxID}); err != nil {
			t.Fatal(err)
		}
	}
	tileWithLabel(t, inbox, "Quarterly numbers")

	lists, meta, history := g.reads()
	if history == 0 {
		t.Error("the inbox caught up without asking history")
	}
	if lists != walkLists {
		t.Errorf("the catch-up listed labels %d more times; it walked again", lists-walkLists)
	}
	if got := meta[len(walkMeta):]; len(got) != 1 || got[0] != "18c2a1b3f4d5e7a1" {
		t.Errorf("the catch-up read metadata for %v, want only the new inbox message", got)
	}
}
