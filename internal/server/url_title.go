package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/josephburnett/gridwell/internal/rpc"
)

// titleCache memoizes URL → page title lookups. Entries persist for the
// lifetime of the process; production cache invalidation can be added later.
type titleCache struct {
	mu      sync.Mutex
	entries map[string]string
	// fetcher is the function used to fetch URL bodies. Tests override it.
	fetcher urlFetcher
}

func newTitleCache() *titleCache {
	return &titleCache{
		entries: map[string]string{},
		fetcher: defaultFetcher,
	}
}

// urlFetcher fetches a URL with a deadline and returns the response body
// (truncated to maxBytes) and an error.
type urlFetcher func(ctx context.Context, url string, maxBytes int64) ([]byte, error)

// defaultFetcher is the production fetcher. It uses a short timeout, refuses
// non-http(s) schemes, and reads at most 64 KiB of body.
func defaultFetcher(ctx context.Context, url string, maxBytes int64) ([]byte, error) {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, errors.New("only http(s) URLs supported")
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "GridwellTitleFetcher/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
	if err != nil {
		return nil, err
	}
	return body, nil
}

// titleRE matches the contents of the first <title>...</title> tag in HTML.
// The regex is tolerant: case-insensitive and allows arbitrary whitespace and
// attributes on the opening tag. It does not parse HTML — for malformed
// pages it may overmatch, but the "first 64 KB" bound limits damage.
var titleRE = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// ExtractTitle returns the contents of the first <title> tag in body, or "".
// Whitespace is collapsed; HTML entity decoding is intentionally minimal:
// only &amp; &lt; &gt; &quot; &#39; are handled.
func ExtractTitle(body []byte) string {
	m := titleRE.FindSubmatch(body)
	if len(m) < 2 {
		return ""
	}
	t := string(m[1])
	t = strings.ReplaceAll(t, "&amp;", "&")
	t = strings.ReplaceAll(t, "&lt;", "<")
	t = strings.ReplaceAll(t, "&gt;", ">")
	t = strings.ReplaceAll(t, "&quot;", `"`)
	t = strings.ReplaceAll(t, "&#39;", "'")
	t = strings.Join(strings.Fields(t), " ")
	return t
}

// Get returns the cached or freshly-fetched title for the URL. On fetch
// failure, returns the empty string and no error: the client renders the
// URL alone (spec §8.3).
func (c *titleCache) Get(ctx context.Context, url string) (string, error) {
	c.mu.Lock()
	if v, ok := c.entries[url]; ok {
		c.mu.Unlock()
		return v, nil
	}
	c.mu.Unlock()

	body, err := c.fetcher(ctx, url, 64*1024)
	title := ""
	if err == nil {
		title = ExtractTitle(body)
	}
	c.mu.Lock()
	c.entries[url] = title
	c.mu.Unlock()
	return title, nil
}

// (Server method) getURLTitle returns the cached/freshly-fetched title.
func (s *Server) getURLTitle(w http.ResponseWriter, r *http.Request) {
	if _, ok := uidOrError(w, r); !ok {
		return
	}
	var req rpc.GetURLTitleRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if req.URL == "" {
		writeError(w, errors.New("url required"))
		return
	}
	title, _ := s.titles.Get(r.Context(), req.URL)
	writeJSON(w, &rpc.GetURLTitleResponse{Title: title})
}
