// Package gitlabfake is a fake GitLab: the paged GET /api/v4/todos the
// gridwell-plugin-gitlab binary talks to, over httptest.
//
// It fakes the SERVICE, never the plugin. A plugin is another repository's
// module and reaches this one only as a spawned binary, so a seam test that
// wants todos behind the adapter stands a fake GitLab up and hands the plugin
// its url and token_file in the config map — exactly the two keys a
// server.yaml plugins: entry carries.
package gitlabfake

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

// Todo is GitLab's todo object as the plugin reads it.
type Todo struct {
	ID         int64  `json:"id"`
	State      string `json:"state"`
	CreatedAt  string `json:"created_at"`
	ActionName string `json:"action_name"`
	TargetType string `json:"target_type"`
	TargetURL  string `json:"target_url"`
	Body       string `json:"body"`
	Project    struct {
		ID                int64  `json:"id"`
		Name              string `json:"name"`
		PathWithNamespace string `json:"path_with_namespace"`
	} `json:"project"`
	Author struct {
		Name     string `json:"name"`
		Username string `json:"username"`
	} `json:"author"`
	Target struct {
		IID   int64  `json:"iid"`
		Title string `json:"title"`
		State string `json:"state"`
	} `json:"target"`
}

// Server is a running fake GitLab.
type Server struct {
	URL string

	mu    sync.Mutex
	todos []Todo
	calls int
}

// New starts a fake GitLab holding todos, stopped at the end of the test.
func New(t *testing.T, todos ...Todo) *Server {
	t.Helper()
	s := &Server{todos: todos}
	hs := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(hs.Close)
	s.URL = hs.URL
	return s
}

// Set replaces what the fake lists from now on: a todo that leaves GitLab.
func (s *Server) Set(todos ...Todo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.todos = todos
}

// Calls counts the API requests served — how many pages the plugin walked.
func (s *Server) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// serve answers GET /api/v4/todos: the todos in the requested state, paged by
// per_page/page with X-Next-Page while more remain, which is the shape the
// real API has.
func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.calls++
	state := r.URL.Query().Get("state")
	var sel []Todo
	for _, td := range s.todos {
		if td.State == state {
			sel = append(sel, td)
		}
	}
	s.mu.Unlock()

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if page < 1 {
		page = 1
	}
	if per < 1 {
		per = len(sel)
	}
	start := min((page-1)*per, len(sel))
	end := min(start+per, len(sel))
	if end < len(sel) {
		w.Header().Set("X-Next-Page", strconv.Itoa(page+1))
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sel[start:end])
}

// Config is the plugin config map pointing gridwell-plugin-gitlab at this
// fake: the url, and a token_file holding a token, since the plugin refuses to
// launch without one. extra keys (such as refresh) are merged in.
func (s *Server) Config(t *testing.T, extra map[string]string) map[string]string {
	t.Helper()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("test-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]string{"url": s.URL, "token_file": tokenFile}
	for k, v := range extra {
		cfg[k] = v
	}
	return cfg
}
