package gitlabapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPageSpeaksTheGitLabWire(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		if r.Header.Get("PRIVATE-TOKEN") != "tok" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("X-Next-Page", "2")
		}
		w.Write([]byte(`[{"id":7,"state":"pending","created_at":"2026-08-18T10:00:00Z","target_type":"Issue","target":{"iid":3,"title":"x"}}]`))
	}))
	defer srv.Close()
	c := New(srv.URL+"/", "tok", nil)
	todos, more, err := c.Page(context.Background(), "pending", 1)
	if err != nil || len(todos) != 1 || todos[0].ID != 7 || todos[0].Target.IID != 3 || !more {
		t.Fatalf("page 1 = %v %v %v", todos, more, err)
	}
	if got.URL.Path != "/api/v4/todos" || got.URL.Query().Get("state") != "pending" || got.URL.Query().Get("per_page") != "100" {
		t.Errorf("request = %s", got.URL)
	}
	if _, more, _ := c.Page(context.Background(), "pending", 2); more {
		t.Error("no X-Next-Page must mean the last page")
	}
	if _, _, err := New(srv.URL, "wrong", nil).Page(context.Background(), "pending", 1); status.Code(err) != codes.PermissionDenied {
		t.Errorf("401 → %v, want PermissionDenied", err)
	}
}

func TestPageMapsOutagesToUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	if _, _, err := New(srv.URL, "t", nil).Page(context.Background(), "done", 1); status.Code(err) != codes.Unavailable {
		t.Errorf("503 → %v", err)
	}
	srv.Close()
	if _, _, err := New(srv.URL, "t", nil).Page(context.Background(), "done", 1); status.Code(err) != codes.Unavailable {
		t.Errorf("refused connection → %v", err)
	}
}
