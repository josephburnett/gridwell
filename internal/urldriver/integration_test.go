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
// driver. Safe for concurrent use because the driver's listener
// callback runs on its own goroutine.
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

func (r *recordingStore) previewLen(tileID int64) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.previews[tileID])
}

func (r *recordingStore) lastURL(tileID int64) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	hist := r.urls[tileID]
	if len(hist) == 0 {
		return ""
	}
	return hist[len(hist)-1]
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

// newDriverForTest sets up a real chromedp-backed Driver for the
// duration of t and a recording store. Pre-flight: confirms the
// browser can actually start (some systems have Chromium on PATH but
// missing libs — skip in that case).
//
// The profile root is managed manually rather than via t.TempDir()
// because Chromium can still be flushing to the profile when the test
// returns, racing with the t.TempDir() cleanup walk and producing
// spurious "directory not empty" failures. Cleanup is registered
// AFTER Shutdown so Chromium's exit completes first.
func newDriverForTest(t *testing.T) (*Driver, *recordingStore) {
	t.Helper()
	_ = requireChromium(t)
	store := newRecordingStore()
	profileRoot, err := os.MkdirTemp("", "gridwell-driver-test-*")
	if err != nil {
		t.Fatal(err)
	}
	d := New(store, Config{
		ProfileRoot:     profileRoot,
		PreviewInterval: 300 * time.Millisecond, // faster polling for tests
	})
	if !d.Available() {
		_ = os.RemoveAll(profileRoot)
		t.Skip("driver reports unavailable despite Chromium on PATH")
	}
	t.Cleanup(func() {
		d.Shutdown()
		// Chromium may still be flushing files at the moment Shutdown
		// returns; a short grace + retry handles the race.
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

func TestIntegrationWakeCaptureRoundtrip(t *testing.T) {
	d, store := newDriverForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><title>Hello</title><body style="background:#abcdef"><h1>hello</h1></body>`))
	}))
	defer srv.Close()

	const userID, tileID int64 = 1, 100
	if err := d.Wake(context.Background(), userID, tileID, srv.URL); err != nil {
		t.Fatalf("wake: %v", err)
	}
	if !d.IsLive(userID, tileID) {
		t.Fatal("IsLive=false after Wake")
	}
	// A preview should land within a reasonable time. Headless Chrome
	// cold start can be a few seconds.
	if !waitFor(15*time.Second, func() bool { return store.previewLen(tileID) > 0 }) {
		t.Fatal("no preview captured within 15s")
	}
	// JPEG bytes should be plausibly large.
	if got := store.previewLen(tileID); got < 200 {
		t.Errorf("preview length = %d bytes, looks suspiciously small", got)
	}
	if err := d.Capture(context.Background(), userID, tileID); err != nil {
		t.Fatalf("capture: %v", err)
	}
	if d.IsLive(userID, tileID) {
		t.Error("IsLive=true after Capture")
	}
}

func TestIntegrationCaptureWithoutWakeIsNoOp(t *testing.T) {
	d, _ := newDriverForTest(t)
	if err := d.Capture(context.Background(), 1, 1); err != nil {
		t.Errorf("capture on dormant tile: %v", err)
	}
}

func TestIntegrationDialogAutoDismiss(t *testing.T) {
	d, store := newDriverForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		// alert() in a script tag would block the page indefinitely
		// if not auto-dismissed. The final document.title write is
		// only reached after the alert is handled.
		_, _ = w.Write([]byte(`<!doctype html>
<title>before</title>
<script>
  alert('hi');
  confirm('really?');
  prompt('what?');
  document.title = 'after-dialogs';
</script>
<body>ok</body>`))
	}))
	defer srv.Close()

	const userID, tileID int64 = 1, 200
	if err := d.Wake(context.Background(), userID, tileID, srv.URL); err != nil {
		t.Fatalf("wake: %v", err)
	}
	// If dialogs hung the page, no preview would ever be written.
	// The preview poll loop is on a 300ms interval; give it ample time.
	if !waitFor(15*time.Second, func() bool { return store.previewLen(tileID) > 0 }) {
		t.Fatal("page appears stuck on a dialog — no preview captured")
	}
}

func TestIntegrationPopupBlocked(t *testing.T) {
	d, _ := newDriverForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html>
<title>popup</title>
<script>
  // window.open should be overridden to return null by the driver's
  // injected hardening script.
  window._popupResult = window.open('https://example.org/');
  document.title = window._popupResult === null ? 'blocked' : 'NOT-blocked';
</script>`))
	}))
	defer srv.Close()

	const userID, tileID int64 = 1, 300
	if err := d.Wake(context.Background(), userID, tileID, srv.URL); err != nil {
		t.Fatalf("wake: %v", err)
	}
	// Evaluate document.title from inside the live tab to confirm
	// window.open was neutered. The tab context is internal to the
	// driver, so reach in via the locked map.
	tc := d.lookupTab(userID, tileID)
	if tc == nil {
		t.Fatal("no tab context")
	}
	if !waitFor(10*time.Second, func() bool {
		title := readTitle(tc.ctx)
		return title == "blocked"
	}) {
		t.Errorf("title after popup attempt = %q, want \"blocked\"", readTitle(tc.ctx))
	}
}

func TestIntegrationNavigationTracksURL(t *testing.T) {
	d, store := newDriverForTest(t)
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/":
			// JS redirect to /next after 100ms.
			_, _ = w.Write([]byte(`<!doctype html><title>start</title>
<script>setTimeout(function(){ window.location.href = '/next'; }, 100);</script>`))
		case "/next":
			_, _ = w.Write([]byte(`<!doctype html><title>done</title>landed`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL

	const userID, tileID int64 = 1, 400
	if err := d.Wake(context.Background(), userID, tileID, srvURL+"/"); err != nil {
		t.Fatalf("wake: %v", err)
	}
	want := srvURL + "/next"
	if !waitFor(10*time.Second, func() bool { return store.lastURL(tileID) == want }) {
		t.Errorf("lastURL = %q, want %q", store.lastURL(tileID), want)
	}
}

// captureSubscriber is a Subscriber that records every frame and
// navigation in slices guarded by a mutex. Used by the streaming
// tests.
type captureSubscriber struct {
	mu     sync.Mutex
	frames [][]byte
	navs   []string
}

func (c *captureSubscriber) SendFrame(jpeg []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.frames = append(c.frames, jpeg)
}

func (c *captureSubscriber) SendNavigation(newURL string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.navs = append(c.navs, newURL)
}

func (c *captureSubscriber) frameCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.frames)
}

func (c *captureSubscriber) navCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.navs)
}

func TestIntegrationSubscriberReceivesFrames(t *testing.T) {
	d, _ := newDriverForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!doctype html><title>stream</title><body style="background:#246">streaming</body>`))
	}))
	defer srv.Close()

	const userID, tileID int64 = 1, 700
	if err := d.Wake(context.Background(), userID, tileID, srv.URL); err != nil {
		t.Fatalf("wake: %v", err)
	}
	sub := &captureSubscriber{}
	unsub := d.Subscribe(userID, tileID, sub)
	defer unsub()

	// Frames should arrive at the streaming cadence (~100ms) — three
	// in under 2 seconds is comfortably more than enough.
	if !waitFor(5*time.Second, func() bool { return sub.frameCount() >= 3 }) {
		t.Errorf("got %d frames in 5s, want >= 3", sub.frameCount())
	}
}

func TestIntegrationSubscriberReceivesNavigation(t *testing.T) {
	d, _ := newDriverForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`<!doctype html><title>start</title>
<script>setTimeout(function(){ window.location.href = '/next'; }, 50);</script>`))
		case "/next":
			_, _ = w.Write([]byte(`<!doctype html><title>done</title>`))
		}
	}))
	defer srv.Close()

	const userID, tileID int64 = 1, 800
	sub := &captureSubscriber{}
	// Subscribe BEFORE wake so we don't miss the initial navigation.
	unsub := d.Subscribe(userID, tileID, sub)
	defer unsub()

	if err := d.Wake(context.Background(), userID, tileID, srv.URL+"/"); err != nil {
		t.Fatalf("wake: %v", err)
	}
	// Expect navigations to both / and /next.
	if !waitFor(10*time.Second, func() bool { return sub.navCount() >= 2 }) {
		t.Errorf("got %d navigations in 10s, want >= 2", sub.navCount())
	}
}

func TestIntegrationForwardInputClick(t *testing.T) {
	d, _ := newDriverForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		// A button that, when clicked, sets document.title to
		// "clicked". The test verifies the title flips after we
		// dispatch a synthetic click at the button's coordinates.
		_, _ = w.Write([]byte(`<!doctype html><title>before</title>
<body>
<button id="b" style="position:fixed;left:0;top:0;width:200px;height:200px;font-size:40px"
  onclick="document.title='clicked'">B</button>
</body>`))
	}))
	defer srv.Close()

	const userID, tileID int64 = 1, 900
	if err := d.Wake(context.Background(), userID, tileID, srv.URL); err != nil {
		t.Fatalf("wake: %v", err)
	}
	// Give the page a moment to lay out.
	time.Sleep(500 * time.Millisecond)

	// Synthesize a click in the button's hit area (centered at 100,100).
	if err := d.ForwardInput(userID, tileID, InputEvent{
		Kind: InputMouseMove, X: 100, Y: 100,
	}); err != nil {
		t.Fatalf("move: %v", err)
	}
	if err := d.ForwardInput(userID, tileID, InputEvent{
		Kind: InputMouseDown, X: 100, Y: 100, Button: MouseButtonLeft,
	}); err != nil {
		t.Fatalf("down: %v", err)
	}
	if err := d.ForwardInput(userID, tileID, InputEvent{
		Kind: InputMouseUp, X: 100, Y: 100, Button: MouseButtonLeft,
	}); err != nil {
		t.Fatalf("up: %v", err)
	}

	tc := d.lookupTab(userID, tileID)
	if tc == nil {
		t.Fatal("no tab")
	}
	if !waitFor(5*time.Second, func() bool { return readTitle(tc.ctx) == "clicked" }) {
		t.Errorf("title = %q, want \"clicked\"", readTitle(tc.ctx))
	}
}

func TestIntegrationFailedLoadStillCapturesFrame(t *testing.T) {
	// Wake at a URL that returns 404; Chromium paints its native
	// "page not found" surface. The driver should still capture a
	// preview — the invariant promises "what was on screen", not
	// "successful content".
	d, store := newDriverForTest(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	const userID, tileID int64 = 1, 500
	if err := d.Wake(context.Background(), userID, tileID, srv.URL+"/missing"); err != nil {
		// Wake may return an error on navigation failure; that's OK
		// as long as the tile becomes live and a frame is captured.
		t.Logf("wake returned %v (acceptable for 404)", err)
	}
	if !waitFor(15*time.Second, func() bool { return store.previewLen(tileID) > 0 }) {
		t.Errorf("no preview after 404 within 15s")
	}
}

// lookupTab is a test-only escape hatch into the driver's internal
// map; used by the popup test to evaluate inside the live tab.
func (d *Driver) lookupTab(userID, tileID int64) *tabContext {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.tabs[liveKey{userID, tileID}]
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
