package urldriver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/go-rod/rod"
)

// requireChromium skips the test cleanly if no Chromium binary is on
// PATH. Used by all integration tests in this package; CI without
// Chromium installed still passes the rest of the package.
func requireChromium(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "chrome", "chrome-headless-shell"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	t.Skip("no Chromium binary on PATH; skipping integration test")
	return ""
}

// recordingStore is a PreviewWriter that records writes from the
// driver. Safe for concurrent use.
type recordingStore struct {
	mu       sync.Mutex
	previews map[int64][]byte
	urls     map[int64][]string // history, newest last
}

func newRecordingStore() *recordingStore {
	return &recordingStore{
		previews: map[int64][]byte{},
		urls:     map[int64][]string{},
	}
}

func (r *recordingStore) SetURLPreview(_ context.Context, tileID int64, jpeg []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.previews[tileID] = jpeg
	return nil
}

func (r *recordingStore) SetURLString(_ context.Context, tileID int64, newURL string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.urls[tileID] = append(r.urls[tileID], newURL)
	return nil
}

// waitFor polls cond every 50ms up to timeout; returns true if cond
// returns true at any point.
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return cond()
}

// newDriverForTest sets up a real chromedp-backed Driver and recording
// store. Profile root is managed manually rather than via t.TempDir()
// because Chromium can still be flushing to the profile when the test
// returns, racing with t.TempDir() cleanup.
func newDriverForTest(t *testing.T) (*Driver, *recordingStore) {
	t.Helper()
	_ = requireChromium(t)
	store := newRecordingStore()
	profileRoot, err := os.MkdirTemp("", "gridwell-driver-test-*")
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(store, Config{
		Browser:         "chromium",
		ProfileOverride: profileRoot,
		Headless:        true,
		StreamInterval:  100 * time.Millisecond,
	})
	if err != nil {
		_ = os.RemoveAll(profileRoot)
		t.Skipf("driver unavailable: %v", err)
	}
	t.Cleanup(func() {
		d.Shutdown()
		for range 5 {
			if err := os.RemoveAll(profileRoot); err == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		_ = os.RemoveAll(profileRoot)
	})
	return d, store
}

// readTitle returns document.title from the given rod page, or "" on
// any failure (so callers can poll with waitFor).
func readTitle(p *rod.Page) string {
	res, err := p.Timeout(1*time.Second).Eval(`() => document.title`)
	if err != nil || res == nil {
		return ""
	}
	return res.Value.Str()
}

// evalInt is a test helper: evaluate a JS expression on the page and
// return the result as an int64, or 0 on failure.
func evalInt(p *rod.Page, expr string) int64 {
	res, err := p.Timeout(1*time.Second).Eval(expr)
	if err != nil || res == nil {
		return 0
	}
	return int64(res.Value.Int())
}

// evalStr is a test helper: evaluate a JS expression on the page and
// return the result as a string, or "" on failure.
func evalStr(p *rod.Page, expr string) string {
	res, err := p.Timeout(1*time.Second).Eval(expr)
	if err != nil || res == nil {
		return ""
	}
	return res.Value.Str()
}

// --- Session API integration tests ---

func TestIntegrationSessionRoundtrip(t *testing.T) {
	d, _ := newDriverForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><title>session</title><body style="background:#abc">hi</body>`))
	}))
	defer srv.Close()

	s, err := d.OpenSession(1000, srv.URL, 800, 600)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer s.Close()

	// First frame arrives within the polling cadence.
	select {
	case f := <-s.Frames():
		if len(f) < 100 {
			t.Errorf("first frame too small: %d bytes", len(f))
		}
	case <-time.After(15 * time.Second):
		t.Fatal("no frame within 15s")
	}

	if !waitFor(5*time.Second, func() bool { return s.LastURL() == srv.URL+"/" }) {
		t.Errorf("LastURL = %q, want %q", s.LastURL(), srv.URL+"/")
	}

	buf, err := s.CaptureFinal(context.Background())
	if err != nil {
		t.Fatalf("CaptureFinal: %v", err)
	}
	if len(buf) < 200 {
		t.Errorf("CaptureFinal too small: %d bytes", len(buf))
	}
}

func TestIntegrationSessionCloseIdempotent(t *testing.T) {
	d, _ := newDriverForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<!doctype html><title>x</title>`))
	}))
	defer srv.Close()

	s, err := d.OpenSession(1001, srv.URL, 800, 600)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	s.Close()
	s.Close() // second call must be a no-op, not panic / deadlock.
	select {
	case <-s.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("Done() did not unblock after Close")
	}
}

func TestIntegrationSessionResize(t *testing.T) {
	d, _ := newDriverForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<!doctype html><title>resize</title>`))
	}))
	defer srv.Close()

	s, err := d.OpenSession(1002, srv.URL, 800, 600)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer s.Close()

	if err := s.Resize(1024, 768); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if !waitFor(5*time.Second, func() bool {
		return evalInt(s.page, `() => window.innerWidth`) == 1024
	}) {
		t.Errorf("window.innerWidth did not become 1024 after Resize")
	}
}

func TestIntegrationSessionInputClick(t *testing.T) {
	d, _ := newDriverForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><title>before</title>
<body>
<button id="b" style="position:fixed;left:0;top:0;width:200px;height:200px;font-size:40px"
  onclick="document.title='clicked'">B</button>
</body>`))
	}))
	defer srv.Close()

	s, err := d.OpenSession(1003, srv.URL, 800, 600)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer s.Close()
	time.Sleep(500 * time.Millisecond)

	for _, ev := range []InputEvent{
		{Kind: InputMouseMove, X: 100, Y: 100},
		{Kind: InputMouseDown, X: 100, Y: 100, Button: MouseButtonLeft},
		{Kind: InputMouseUp, X: 100, Y: 100, Button: MouseButtonLeft},
	} {
		if err := s.Input(ev); err != nil {
			t.Fatalf("Input %s: %v", ev.Kind, err)
		}
	}

	if !waitFor(5*time.Second, func() bool { return readTitle(s.page) == "clicked" }) {
		t.Errorf("title = %q, want \"clicked\"", readTitle(s.page))
	}
}

// TestIntegrationSessionInputKeyboard exercises text-typing into a
// focused <input>. Regression test for the bug where keystrokes
// reached the page as keydown events but no character actually
// landed in the input value — the result of dispatching CDP
// Input.dispatchKeyEvent without the Text / UnmodifiedText fields.
// Also covers Enter (special-key VK code path).
func TestIntegrationSessionInputKeyboard(t *testing.T) {
	d, _ := newDriverForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><title>kb</title>
<body>
<input id="i" autofocus style="position:fixed;left:0;top:0;width:300px;height:60px;font-size:30px">
<script>
  // Submit-style handler: Enter while focused on the input updates title.
  document.getElementById('i').addEventListener('keydown', function(ev){
    if (ev.key === 'Enter') document.title = 'submit:' + ev.target.value;
  });
</script>
</body>`))
	}))
	defer srv.Close()

	s, err := d.OpenSession(1020, srv.URL, 800, 600)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer s.Close()
	// Give autofocus a moment to settle.
	if !waitFor(5*time.Second, func() bool {
		return evalStr(s.page, `() => document.activeElement && document.activeElement.id`) == "i"
	}) {
		t.Fatalf("input never got focus; activeElement=%q", evalStr(s.page, `() => document.activeElement && document.activeElement.id`))
	}

	type keystroke struct{ key, code string }
	for _, k := range []keystroke{
		{"h", "KeyH"},
		{"i", "KeyI"},
		{" ", "Space"},
		{"7", "Digit7"},
	} {
		if err := s.Input(InputEvent{Kind: InputKeyDown, Key: k.key, Code: k.code}); err != nil {
			t.Fatalf("keydown %q: %v", k.key, err)
		}
		if err := s.Input(InputEvent{Kind: InputKeyUp, Key: k.key, Code: k.code}); err != nil {
			t.Fatalf("keyup %q: %v", k.key, err)
		}
	}

	if !waitFor(5*time.Second, func() bool {
		return evalStr(s.page, `() => document.getElementById('i').value`) == "hi 7"
	}) {
		t.Errorf("input.value = %q, want %q",
			evalStr(s.page, `() => document.getElementById('i').value`), "hi 7")
	}

	// Enter triggers the keydown handler that writes a title.
	if err := s.Input(InputEvent{Kind: InputKeyDown, Key: "Enter", Code: "Enter"}); err != nil {
		t.Fatalf("Enter keydown: %v", err)
	}
	if err := s.Input(InputEvent{Kind: InputKeyUp, Key: "Enter", Code: "Enter"}); err != nil {
		t.Fatalf("Enter keyup: %v", err)
	}
	if !waitFor(5*time.Second, func() bool { return readTitle(s.page) == "submit:hi 7" }) {
		t.Errorf("title = %q, want \"submit:hi 7\" — Enter key not recognized", readTitle(s.page))
	}
}

func TestIntegrationSessionNavigation(t *testing.T) {
	d, _ := newDriverForTest(t)
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`<!doctype html><title>a</title>
<script>setTimeout(function(){ window.location.href = '/next'; }, 50);</script>`))
		case "/next":
			_, _ = w.Write([]byte(`<!doctype html><title>b</title>`))
		}
	}))
	defer srv.Close()
	srvURL = srv.URL

	s, err := d.OpenSession(1004, srvURL+"/", 800, 600)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer s.Close()

	want := srvURL + "/next"
	deadline := time.After(10 * time.Second)
	for {
		select {
		case u := <-s.Navs():
			if u == want {
				return
			}
		case <-deadline:
			t.Fatalf("never observed nav to %q; LastURL=%q", want, s.LastURL())
		}
	}
}

func TestIntegrationSessionDialogAutoDismiss(t *testing.T) {
	d, _ := newDriverForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		// alert/confirm/prompt would block the page if not auto-dismissed.
		// The title write after them only fires if all dialogs resolved.
		_, _ = w.Write([]byte(`<!doctype html>
<title>before</title>
<script>
  alert('hi');
  confirm('really?');
  prompt('what?');
  document.title = 'after-dialogs';
</script>`))
	}))
	defer srv.Close()

	s, err := d.OpenSession(1005, srv.URL, 800, 600)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer s.Close()
	if !waitFor(10*time.Second, func() bool { return readTitle(s.page) == "after-dialogs" }) {
		t.Errorf("page appears stuck on a dialog; title = %q", readTitle(s.page))
	}
}

func TestIntegrationSessionPopupBlocked(t *testing.T) {
	d, _ := newDriverForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><title>popup</title>
<script>
  window._popupResult = window.open('https://example.org/');
  document.title = window._popupResult === null ? 'blocked' : 'NOT-blocked';
</script>`))
	}))
	defer srv.Close()

	s, err := d.OpenSession(1006, srv.URL, 800, 600)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer s.Close()
	if !waitFor(10*time.Second, func() bool { return readTitle(s.page) == "blocked" }) {
		t.Errorf("title after popup attempt = %q, want \"blocked\"", readTitle(s.page))
	}
}

// TestIntegrationSessionSurvivesCrossOriginNav is the test that proves
// the chromedp→rod migration was worth doing. Two httptest servers on
// different ports = different origins; page A has a link to page B.
// Clicking the link makes Chromium swap renderer processes (Site
// Isolation). Before rod, this killed the per-tab chromedp.Context and
// every subsequent input failed with "context canceled". With rod,
// the *Page rebinds across the swap and the Session stays usable.
func TestIntegrationSessionSurvivesCrossOriginNav(t *testing.T) {
	d, _ := newDriverForTest(t)

	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><title>B</title>
<body>
<button id="b" style="position:fixed;left:0;top:0;width:200px;height:200px;font-size:40px"
  onclick="document.title='B-clicked'">B</button>
</body>`))
	}))
	defer srvB.Close()

	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><title>A</title>
<body>
<a id="link" href="` + srvB.URL + `/" style="position:fixed;left:0;top:0;width:200px;height:200px;font-size:40px;display:block">go</a>
</body>`))
	}))
	defer srvA.Close()

	s, err := d.OpenSession(1010, srvA.URL+"/", 800, 600)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer s.Close()
	time.Sleep(500 * time.Millisecond)

	// Click the link → cross-origin navigation, renderer swap.
	for _, ev := range []InputEvent{
		{Kind: InputMouseMove, X: 100, Y: 100},
		{Kind: InputMouseDown, X: 100, Y: 100, Button: MouseButtonLeft},
		{Kind: InputMouseUp, X: 100, Y: 100, Button: MouseButtonLeft},
	} {
		if err := s.Input(ev); err != nil {
			t.Fatalf("first-click Input %s: %v", ev.Kind, err)
		}
	}

	// Wait for the cross-origin nav to land.
	if !waitFor(10*time.Second, func() bool { return s.LastURL() == srvB.URL+"/" }) {
		t.Fatalf("never navigated to %q; LastURL=%q", srvB.URL+"/", s.LastURL())
	}
	// Give the new page a moment to lay out.
	time.Sleep(500 * time.Millisecond)

	// Critical: input still works post-swap. Click the button on
	// page B. With chromedp this would return context.Canceled.
	for _, ev := range []InputEvent{
		{Kind: InputMouseMove, X: 100, Y: 100},
		{Kind: InputMouseDown, X: 100, Y: 100, Button: MouseButtonLeft},
		{Kind: InputMouseUp, X: 100, Y: 100, Button: MouseButtonLeft},
	} {
		if err := s.Input(ev); err != nil {
			t.Fatalf("post-swap Input %s: %v — Session did not survive cross-origin nav", ev.Kind, err)
		}
	}
	if !waitFor(5*time.Second, func() bool { return readTitle(s.page) == "B-clicked" }) {
		t.Errorf("post-swap title = %q, want \"B-clicked\"", readTitle(s.page))
	}

	// And the page is still alive (Done has not fired).
	select {
	case <-s.Done():
		t.Fatal("Session.Done fired after cross-origin nav — Page should survive")
	default:
	}
}

func TestIntegrationSessionFailedLoad(t *testing.T) {
	d, _ := newDriverForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	s, err := d.OpenSession(1007, srv.URL+"/missing", 800, 600)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer s.Close()

	// Chromium paints its own "page not found" surface; we should still
	// get a frame.
	select {
	case f := <-s.Frames():
		if len(f) < 100 {
			t.Errorf("frame too small after 404: %d", len(f))
		}
	case <-time.After(15 * time.Second):
		t.Fatal("no frame after 404 within 15s")
	}
}

// TestIntegrationSessionFollowsTargetBlank verifies that clicking a
// target="_blank" link causes the session to swap to the new tab and
// close the old one. Without this swap, target="_blank" links open
// invisibly in Xvfb and the user perceives the click as dead.
func TestIntegrationSessionFollowsTargetBlank(t *testing.T) {
	d, _ := newDriverForTest(t)

	// Destination page served by srvB. The session should end up here.
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><title>B-after-blank</title>`))
	}))
	defer srvB.Close()

	// Source page served by srvA, with a target="_blank" link to srvB.
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><title>A-before</title>
<body>
<a id="link" target="_blank" href="` + srvB.URL + `/" style="position:fixed;left:0;top:0;width:200px;height:200px;font-size:40px;display:block">go</a>
</body>`))
	}))
	defer srvA.Close()

	s, err := d.OpenSession(1030, srvA.URL+"/", 800, 600)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer s.Close()
	time.Sleep(500 * time.Millisecond)

	for _, ev := range []InputEvent{
		{Kind: InputMouseMove, X: 100, Y: 100},
		{Kind: InputMouseDown, X: 100, Y: 100, Button: MouseButtonLeft},
		{Kind: InputMouseUp, X: 100, Y: 100, Button: MouseButtonLeft},
	} {
		if err := s.Input(ev); err != nil {
			t.Fatalf("click Input %s: %v", ev.Kind, err)
		}
	}

	if !waitFor(10*time.Second, func() bool { return s.LastURL() == srvB.URL+"/" }) {
		t.Fatalf("never followed _blank to %q; LastURL=%q", srvB.URL+"/", s.LastURL())
	}
	if !waitFor(5*time.Second, func() bool { return readTitle(s.page) == "B-after-blank" }) {
		t.Errorf("post-swap title = %q, want \"B-after-blank\"", readTitle(s.page))
	}

	// Only one page should remain in the browser — the new one. The
	// original (with target="_blank" anchor) must have been closed.
	pages, err := s.browser.Pages()
	if err != nil {
		t.Fatalf("list pages: %v", err)
	}
	if len(pages) != 1 {
		titles := make([]string, 0, len(pages))
		for _, p := range pages {
			titles = append(titles, readTitle(p))
		}
		t.Errorf("got %d pages, want 1: titles=%v", len(pages), titles)
	}
}

// TestIntegrationSessionHistoryBack pins the wire-level back-button:
// the client sends InputHistoryBack, the driver dispatches history.back()
// on the page, and the page reverts to the previous URL.
func TestIntegrationSessionHistoryBack(t *testing.T) {
	d, _ := newDriverForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/a":
			_, _ = w.Write([]byte(`<!doctype html><title>A</title>`))
		case "/b":
			_, _ = w.Write([]byte(`<!doctype html><title>B</title>`))
		}
	}))
	defer srv.Close()

	s, err := d.OpenSession(1040, srv.URL+"/a", 800, 600)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer s.Close()
	time.Sleep(300 * time.Millisecond)

	// Drive an in-tab navigation A → B via JS (no need for a real link;
	// what we're testing is the back direction).
	if _, err := s.page.Eval(`() => { location.href = "` + srv.URL + `/b" }`); err != nil {
		t.Fatalf("nav forward: %v", err)
	}
	if !waitFor(5*time.Second, func() bool { return readTitle(s.page) == "B" }) {
		t.Fatalf("never reached B; title=%q", readTitle(s.page))
	}

	if err := s.Input(InputEvent{Kind: InputHistoryBack}); err != nil {
		t.Fatalf("InputHistoryBack: %v", err)
	}
	if !waitFor(5*time.Second, func() bool { return readTitle(s.page) == "A" }) {
		t.Errorf("after back, title=%q, want \"A\"", readTitle(s.page))
	}
}

// TestIntegrationSessionViewportSurvivesSwap pins the fix for a
// regression where the tab opened via target="_blank" got Chromium's
// default viewport instead of the tile's current pane size, producing
// a stretched JPEG until the user reloaded by ascending/descending.
func TestIntegrationSessionViewportSurvivesSwap(t *testing.T) {
	d, _ := newDriverForTest(t)

	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><title>B</title>`))
	}))
	defer srvB.Close()

	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><title>A</title>
<body><a id="link" target="_blank" href="` + srvB.URL + `/" style="position:fixed;left:0;top:0;width:200px;height:200px;display:block">go</a></body>`))
	}))
	defer srvA.Close()

	s, err := d.OpenSession(1031, srvA.URL+"/", 800, 600)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer s.Close()
	time.Sleep(300 * time.Millisecond)

	// Simulate a pane resize before the click — the tile is now 1234x789.
	if err := s.Resize(1234, 789); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if !waitFor(3*time.Second, func() bool {
		return evalInt(s.page, `() => window.innerWidth`) == 1234
	}) {
		t.Fatalf("pre-swap viewport never reached 1234; got %d",
			evalInt(s.page, `() => window.innerWidth`))
	}

	// Click the target="_blank" link.
	for _, ev := range []InputEvent{
		{Kind: InputMouseMove, X: 100, Y: 100},
		{Kind: InputMouseDown, X: 100, Y: 100, Button: MouseButtonLeft},
		{Kind: InputMouseUp, X: 100, Y: 100, Button: MouseButtonLeft},
	} {
		if err := s.Input(ev); err != nil {
			t.Fatalf("click Input %s: %v", ev.Kind, err)
		}
	}
	if !waitFor(10*time.Second, func() bool { return s.LastURL() == srvB.URL+"/" }) {
		t.Fatalf("never followed _blank; LastURL=%q", s.LastURL())
	}

	// New tab must carry the 1234x789 viewport, not Chromium's default.
	if !waitFor(3*time.Second, func() bool {
		return evalInt(s.page, `() => window.innerWidth`) == 1234 &&
			evalInt(s.page, `() => window.innerHeight`) == 789
	}) {
		t.Errorf("post-swap viewport = %dx%d, want 1234x789",
			evalInt(s.page, `() => window.innerWidth`),
			evalInt(s.page, `() => window.innerHeight`))
	}
}
