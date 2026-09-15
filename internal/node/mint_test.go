package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The mint is the one config write and it writes only the ids. A second boot
// of a home that never declared web.bind still sees none, so the desktop
// sidecar's --bind-default keeps winning on every launch rather than the first
// one only, and the file stays as the user left it.
func TestBuildConfigMintsOnlyTheIDs(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, "server.yaml")
	first, err := BuildConfig(home, cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildConfig(home, cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("the minted id must persist: %q then %q", first.ID, second.ID)
	}
	if second.Web.BindSet {
		t.Fatalf("the first boot wrote a web.bind the user never declared: %+v", second.Web)
	}
	raw, _ := os.ReadFile(cfgPath)
	if strings.TrimSpace(string(raw)) != "id: "+first.ID {
		t.Fatalf("server.yaml carries more than the minted id:\n%s", raw)
	}
}
