// Package gitlabapi is the thin HTTP half of the gitlab todos provider:
// one pager over GET /api/v4/todos. It knows the wire (token header,
// per_page, X-Next-Page) and nothing about weeks, memory, or tiles.
package gitlabapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/josephburnett/gridwell/plugins/gitlab/todos"
)

// PerPage is the page size asked of GitLab (its maximum).
const PerPage = 100

// Client pages one GitLab instance's todo list for one token.
type Client struct {
	base  string // "https://gitlab.com" — no trailing slash
	token string
	http  *http.Client
}

// New builds a client. A nil httpClient uses http.DefaultClient.
func New(base, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{base: strings.TrimRight(base, "/"), token: token, http: httpClient}
}

// Page implements todos.Source. Failures map to gRPC codes the node
// understands: a network or 5xx/429 failure is Unavailable ("not right
// now" — the node serves its remembered listing, stamped stale); 401/
// 403 is PermissionDenied (a verdict; it surfaces).
func (c *Client) Page(ctx context.Context, state string, page int) ([]todos.Todo, bool, error) {
	q := url.Values{}
	q.Set("state", state)
	q.Set("per_page", strconv.Itoa(PerPage))
	q.Set("page", strconv.Itoa(page))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/v4/todos?"+q.Encode(), nil)
	if err != nil {
		return nil, false, status.Errorf(codes.InvalidArgument, "gitlab: %v", err)
	}
	req.Header.Set("PRIVATE-TOKEN", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false, status.Errorf(codes.Unavailable, "gitlab: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, false, status.Errorf(codes.Unavailable, "gitlab: read: %v", err)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, false, status.Errorf(codes.PermissionDenied, "gitlab: %s (check the token's read_api scope)", resp.Status)
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return nil, false, status.Errorf(codes.Unavailable, "gitlab: %s", resp.Status)
	case resp.StatusCode != http.StatusOK:
		return nil, false, status.Errorf(codes.Internal, "gitlab: %s: %s", resp.Status, trim(body))
	}
	var out []todos.Todo
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, false, status.Errorf(codes.Internal, "gitlab: decode todos: %v", err)
	}
	return out, resp.Header.Get("X-Next-Page") != "", nil
}

func trim(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return fmt.Sprintf("%q", s)
}
