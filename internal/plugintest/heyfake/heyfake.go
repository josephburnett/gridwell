// Package heyfake is a fake HEY CLI: the three commands gridwell-plugin-hey
// runs, answered from boxes, threads, failures and a live feed the test sets,
// so a seam test drives the shipped binary with no HEY account.
//
// It fakes the CLI, never the plugin. The executable is this test binary,
// linked into a temp directory as `hey` and handed over as the plugin's
// `binary` config key; started under that name, this package's init becomes
// the CLI and asks the test's in-process fake for its answer over loopback.
// So the plugin's run, argv, environment, exit code and JSON parse are all
// real, and each test owns its own HEY. The contract is the CLI's own:
//
//	hey box view <box> --json --all     the response envelope, data.postings
//	hey thread read <topic-id> --html   a bare HTML5 document, no envelope
//	hey watch --events <changes>        one JSON line per change, until it exits
//
// A refusal is the CLI's exit code with its reason on stderr, the envelope
// for `box view` and a bare line for `thread read`.
package heyfake

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The CLI's exit codes (`hey help exit-codes`) a test stages.
const (
	ExitUsage         = 1
	ExitNotFound      = 2
	ExitAuthRequired  = 3
	ExitForbidden     = 4
	ExitRateLimited   = 5
	ExitNetworkFailed = 6
	ExitServerError   = 7
)

// Name is the executable's base name, which is how init tells the CLI from
// the test.
const Name = "hey"

const (
	addrFile      = "hey.addr"
	exitTrailer   = "Heyfake-Exit"
	stderrTrailer = "Heyfake-Stderr"
)

// Thread is one HEY thread as `box view` lists it and `thread read` opens it.
type Thread struct {
	TopicID          int64
	Subject, Summary string
	From, Email      string
	Seen             bool
	Created          time.Time
	// HTML is what `thread read` answers; empty is a one-article document
	// holding the summary.
	HTML string
}

// boxes are the selectors the CLI knows. Any other is not found.
var selectors = []string{"imbox", "laterbox", "asidebox", "feedbox", "trailbox", "bubblebox"}

// CLI is one test's HEY.
type CLI struct {
	path string
	stop chan struct{}

	mu      sync.Mutex
	boxes   map[string][]Thread
	failing map[string]failure
	feeds   []*Feed
	arrived chan struct{}
}

type failure struct {
	code   int
	reason string
}

// New links the fake CLI into a temp directory and serves it until the end
// of the test. Every box starts empty.
func New(t *testing.T) *CLI {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	c := &CLI{
		path:    filepath.Join(dir, Name),
		stop:    make(chan struct{}),
		boxes:   map[string][]Thread{},
		failing: map[string]failure{},
		arrived: make(chan struct{}, 1),
	}
	hs := httptest.NewServer(http.HandlerFunc(c.serve))
	// A feed holds its request open; stop ends it, or Close waits on it.
	t.Cleanup(func() { close(c.stop); hs.Close() })
	if err := os.WriteFile(filepath.Join(dir, addrFile), []byte(hs.URL), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, c.path); err != nil {
		t.Fatal(err)
	}
	return c
}

// Path is the CLI's absolute path.
func (c *CLI) Path() string { return c.path }

// Config is the plugin config map naming this CLI, with extra merged in.
func (c *CLI) Config(extra map[string]string) map[string]string {
	cfg := map[string]string{"binary": c.path}
	for k, v := range extra {
		cfg[k] = v
	}
	return cfg
}

// Remove takes the executable away, as uninstalling the CLI does. A run
// already started carries on.
func (c *CLI) Remove(t *testing.T) {
	t.Helper()
	if err := os.Remove(c.path); err != nil {
		t.Fatal(err)
	}
}

// Restore puts the executable back.
func (c *CLI) Restore(t *testing.T) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, c.path); err != nil {
		t.Fatal(err)
	}
}

// SetBox replaces what one box lists from now on.
func (c *CLI) SetBox(box string, threads ...Thread) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.boxes[box] = append([]Thread(nil), threads...)
}

// FailBox makes every `box view` of box exit with code and reason, until
// HealBox.
func (c *CLI) FailBox(box string, code int, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failing[box] = failure{code, reason}
}

// HealBox lets box answer again.
func (c *CLI) HealBox(box string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.failing, box)
}

// Feed is one running `hey watch`, which says nothing until the test sends.
type Feed struct {
	lines chan string
	end   chan int
	done  chan struct{}
}

// AwaitFeed answers the oldest feed the plugin started that no earlier call
// took, waiting for one.
func (c *CLI) AwaitFeed(t *testing.T) *Feed {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		c.mu.Lock()
		if len(c.feeds) > 0 {
			f := c.feeds[0]
			c.feeds = c.feeds[1:]
			c.mu.Unlock()
			return f
		}
		c.mu.Unlock()
		select {
		case <-c.arrived:
		case <-deadline:
			t.Fatal("heyfake: the plugin started no `hey watch`")
		}
	}
}

// Send prints one line on the feed.
func (f *Feed) Send(t *testing.T, line string) {
	t.Helper()
	select {
	case f.lines <- line:
	case <-f.done:
		t.Fatalf("heyfake: the feed ended before %.80q", line)
	}
}

// Ready prints the line the CLI prints once it is following the account.
func (f *Feed) Ready(t *testing.T) {
	t.Helper()
	f.Send(t, `{"change":"ready","at":"`+time.Now().UTC().Format(time.RFC3339Nano)+`"}`)
}

// End makes the feed exit with code.
func (f *Feed) End(code int) {
	select {
	case f.end <- code:
	case <-f.done:
	}
}

// request is one run, as the executable forwards it.
type request struct {
	Args           []string `json:"args"`
	Noninteractive string   `json:"noninteractive"`
}

func (c *CLI) serve(w http.ResponseWriter, r *http.Request) {
	var req request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Trailer", exitTrailer+", "+stderrTrailer)
	exit := func(code int, stdout, stderr string) {
		_, _ = io.WriteString(w, stdout)
		w.Header().Set(exitTrailer, strconv.Itoa(code))
		w.Header().Set(stderrTrailer, url.QueryEscape(stderr))
	}
	// A prompt with nothing to read it would hang the plugin's walk, so a run
	// that could prompt is refused.
	if req.Noninteractive != "1" {
		exit(ExitUsage, "", "Error: interactive\n")
		return
	}
	a := req.Args
	switch {
	case len(a) == 5 && a[0] == "box" && a[1] == "view" && a[3] == "--json" && a[4] == "--all":
		exit(c.boxView(a[2]))
	case len(a) == 4 && a[0] == "thread" && a[1] == "read" && a[3] == "--html":
		exit(c.threadRead(a[2]))
	case len(a) >= 1 && a[0] == "watch":
		c.watch(w, r, exit)
	default:
		exit(ExitUsage, "", "Error: unknown command\n")
	}
}

type posting struct {
	ID        int64     `json:"id"`
	TopicID   int64     `json:"topic_id"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Summary   string    `json:"summary"`
	Seen      bool      `json:"seen"`
	CreatedAt time.Time `json:"created_at"`
	Creator   struct {
		Name         string `json:"name"`
		EmailAddress string `json:"email_address"`
	} `json:"creator"`
}

func (c *CLI) boxView(box string) (int, string, string) {
	c.mu.Lock()
	threads, f := c.boxes[box], c.failing[box]
	failed := f.code != 0
	c.mu.Unlock()
	known := false
	for _, b := range selectors {
		known = known || b == box
	}
	switch {
	case failed:
		return f.code, "", envelopeErr(f.reason)
	case !known:
		return ExitNotFound, "", envelopeErr("no box of kind " + box)
	}
	postings := []posting{}
	for _, t := range threads {
		p := posting{ID: t.TopicID, TopicID: t.TopicID, Kind: "topic", Name: t.Subject, Summary: t.Summary, Seen: t.Seen, CreatedAt: t.Created}
		p.Creator.Name, p.Creator.EmailAddress = t.From, t.Email
		postings = append(postings, p)
	}
	out, err := json.Marshal(map[string]any{
		"ok":      true,
		"data":    map[string]any{"kind": box, "name": box, "postings": postings},
		"summary": fmt.Sprintf("%d threads", len(postings)),
	})
	if err != nil {
		return ExitServerError, "", envelopeErr(err.Error())
	}
	return 0, string(out) + "\n", ""
}

func envelopeErr(reason string) string {
	out, _ := json.Marshal(map[string]any{"ok": false, "error": reason})
	return string(out) + "\n"
}

func (c *CLI) threadRead(id string) (int, string, string) {
	topic, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return ExitUsage, "", "Error: invalid topic id\n"
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, threads := range c.boxes {
		for _, t := range threads {
			if t.TopicID != topic {
				continue
			}
			if t.HTML != "" {
				return 0, t.HTML, ""
			}
			return 0, `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>` + t.Subject + `</title></head><body>
<article id="entry-1" data-entry-id="1"><header>` + t.From + `</header><p>` + t.Summary + `</p></article>
</body></html>
`, ""
		}
	}
	return ExitNotFound, "", "Error: thread not found\n"
}

// watch holds the run open, printing what the test sends, until the test ends
// it, the plugin kills the CLI, or the test is over.
func (c *CLI) watch(w http.ResponseWriter, r *http.Request, exit func(int, string, string)) {
	f := &Feed{lines: make(chan string), end: make(chan int), done: make(chan struct{})}
	defer close(f.done)
	fl, _ := w.(http.Flusher)
	w.WriteHeader(http.StatusOK)
	if fl != nil {
		fl.Flush()
	}
	c.mu.Lock()
	c.feeds = append(c.feeds, f)
	c.mu.Unlock()
	select {
	case c.arrived <- struct{}{}:
	default:
	}
	for {
		select {
		case line := <-f.lines:
			_, _ = io.WriteString(w, line+"\n")
			if fl != nil {
				fl.Flush()
			}
		case code := <-f.end:
			exit(code, "", "")
			return
		case <-r.Context().Done():
			return
		case <-c.stop:
			return
		}
	}
}

// init is the CLI when this binary was started as it.
func init() {
	if filepath.Base(os.Args[0]) == Name {
		os.Exit(run())
	}
}

// run forwards one run to the test's fake and prints its answer, stdout as it
// arrives. A fake that cannot be reached is the CLI's own server failure.
func run() int {
	addr, err := os.ReadFile(filepath.Join(filepath.Dir(os.Args[0]), addrFile))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: heyfake: %v\n", err)
		return ExitServerError
	}
	body, err := json.Marshal(request{Args: os.Args[1:], Noninteractive: os.Getenv("HEY_NONINTERACTIVE")})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: heyfake: %v\n", err)
		return ExitServerError
	}
	cl := &http.Client{Transport: &http.Transport{}}
	resp, err := cl.Post(strings.TrimSpace(string(addr))+"/run", "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: heyfake: %v\n", err)
		return ExitServerError
	}
	defer resp.Body.Close()
	if _, err := io.Copy(os.Stdout, resp.Body); err != nil {
		fmt.Fprintf(os.Stderr, "Error: heyfake: %v\n", err)
		return ExitServerError
	}
	stderr, _ := url.QueryUnescape(resp.Trailer.Get(stderrTrailer))
	fmt.Fprint(os.Stderr, stderr)
	code, err := strconv.Atoi(resp.Trailer.Get(exitTrailer))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: heyfake: no exit code\n")
		return ExitServerError
	}
	return code
}
