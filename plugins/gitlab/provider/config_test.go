package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	cpv1 "github.com/josephburnett/gridwell/api/gen/contentprovider/v1"
	"github.com/josephburnett/gridwell/plugins/gitlab/todos"
)

func TestFromConfigRefusalsSurfaceThroughList(t *testing.T) {
	cases := map[string]map[string]string{
		"token_file not configured": {},
		"token_file: open":          {"token_file": filepath.Join(t.TempDir(), "missing")},
		"is empty":                  {"token_file": writeTemp(t, "  \n")},
		"not a duration":            {"token_file": writeTemp(t, "tok"), "refresh": "soon"},
	}
	for want, cfg := range cases {
		p := FromConfig(cfg)
		_, err := p.List(context.Background(), &cpv1.ListRequest{Context: todos.RootContext})
		if status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), want) {
			t.Errorf("cfg %v → %v, want FailedPrecondition containing %q", cfg, err, want)
		}
	}
}

func TestFromConfigComposesTheClient(t *testing.T) {
	p := FromConfig(map[string]string{"token_file": writeTemp(t, "tok\n"), "refresh": "5m", "label": "work todos", "url": "https://gl.example/"})
	if p.srcErr != nil || p.src == nil || p.refresh != 5*time.Minute {
		t.Errorf("provider = src %v err %v refresh %v", p.src, p.srcErr, p.refresh)
	}
	info, _ := p.Info(context.Background(), &cpv1.InfoRequest{})
	if info.DisplayName != "work todos" {
		t.Errorf("label = %q", info.DisplayName)
	}
}

func writeTemp(t *testing.T, s string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
