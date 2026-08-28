package plugin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
)

// A config the plugin cannot run on is FromConfig's error — the one
// verdict both doors (guest.Main, the loader's Factory) turn into a
// launch that stops with the reason.
func TestFromConfigRefusesBadConfig(t *testing.T) {
	cases := map[string]map[string]string{
		"token_file not configured": {},
		"token_file: open":          {"token_file": filepath.Join(t.TempDir(), "missing")},
		"is empty":                  {"token_file": writeTemp(t, "  \n")},
		"not a duration":            {"token_file": writeTemp(t, "tok"), "refresh": "soon"},
	}
	for want, cfg := range cases {
		impl, err := FromConfig(cfg)
		if impl != nil || err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("cfg %v → %v, %v; want a refusal containing %q", cfg, impl, err, want)
		}
	}
}

func TestFromConfigComposesTheClient(t *testing.T) {
	impl, err := FromConfig(map[string]string{"token_file": writeTemp(t, "tok\n"), "refresh": "5m", "url": "https://gl.example/"})
	if err != nil {
		t.Fatal(err)
	}
	p := impl.(*Plugin)
	if p.src == nil || p.refresh != 5*time.Minute {
		t.Errorf("plugin = src %v refresh %v", p.src, p.refresh)
	}
	// The user-facing name is server.yaml's `name` (the registry label);
	// the plugin's own DisplayName is only the fallback.
	info, _ := p.Info(context.Background(), &pluginv1.InfoRequest{})
	if info.DisplayName != displayName || info.Kind != Kind {
		t.Errorf("info = %v", info)
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
