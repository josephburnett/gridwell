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

	"github.com/chromedp/chromedp"
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

func (r *recordingStore) SetURLPreview(_ context.Context, _, tileID int64, jpeg []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.previews[tileID] = jpeg
	return nil
}

func (r *recordingStore) SetURLString(_ context.Context, _, tileID int64, newURL string) error {
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
	d := New(store, Config{
		ProfileRoot:    profileRoot,
		StreamInterval: 100 * time.Millisecond,
	})
	if !d.Available() {
		_ = os.RemoveAll(profileRoot)
		t.Skip("driver reports unavailable despite Chromium on PATH")
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

// readTitle returns document.title from the given chromedp tab
// context, or "" on any failure (so callers can poll with waitFor).
func readTitle(tabCtx context.Context) string {
	ctx, cancel := context.WithTimeout(tabCtx, 1*time.Second)
	defer cancel()
	var title string
	if err := chromedp.Run(ctx, chromedp.Title(&title)); err != nil {
		return ""
	}
	return title
}

// --- Session API integration tests ---

func TestIntegrationSessionRoundtrip(t *testing.T) {
	d, _ := newDriverForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><title>session</title><body style="background:#abc">hi</body>`))
	}))
	defer srv.Close()

	s, err := d.OpenSession(1, 1000, srv.URL, 800, 600)
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

	s, err := d.OpenSession(1, 1001, srv.URL, 800, 600)
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

	s, err := d.OpenSession(1, 1002, srv.URL, 800, 600)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer s.Close()

	if err := s.Resize(1024, 768); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if !waitFor(5*time.Second, func() bool {
		var w int64
		ctx, cancel := context.WithTimeout(s.ctx, 1*time.Second)
		defer cancel()
		if err := chromedp.Run(ctx, chromedp.Evaluate("window.innerWidth", &w)); err != nil {
			return false
		}
		return w == 1024
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

	s, err := d.OpenSession(1, 1003, srv.URL, 800, 600)
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

	if !waitFor(5*time.Second, func() bool { return readTitle(s.ctx) == "clicked" }) {
		t.Errorf("title = %q, want \"clicked\"", readTitle(s.ctx))
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

	s, err := d.OpenSession(1, 1004, srvURL+"/", 800, 600)
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

	s, err := d.OpenSession(1, 1005, srv.URL, 800, 600)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer s.Close()
	if !waitFor(10*time.Second, func() bool { return readTitle(s.ctx) == "after-dialogs" }) {
		t.Errorf("page appears stuck on a dialog; title = %q", readTitle(s.ctx))
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

	s, err := d.OpenSession(1, 1006, srv.URL, 800, 600)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer s.Close()
	if !waitFor(10*time.Second, func() bool { return readTitle(s.ctx) == "blocked" }) {
		t.Errorf("title after popup attempt = %q, want \"blocked\"", readTitle(s.ctx))
	}
}

func TestIntegrationSessionFailedLoad(t *testing.T) {
	d, _ := newDriverForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	s, err := d.OpenSession(1, 1007, srv.URL+"/missing", 800, 600)
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
