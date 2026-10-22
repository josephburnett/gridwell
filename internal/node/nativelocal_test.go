package node

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/local/store"
)

// A host whose tmux will not start refuses a shell with the reason, so the
// shell's exit names the cause rather than only the log.
func TestNoTmuxRefusesAShellWithTheReason(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", blocked) // tmux.New cannot make its config dir under a file
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	err = newHome(st, "a123456", "").OpenShell(context.Background(),
		func() (*gridwellv1.OpenShellRequest, error) { return nil, io.EOF },
		func(*gridwellv1.OpenShellResponse) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "live shells are off on this host") || !strings.Contains(err.Error(), "tmux: config dir") {
		t.Errorf("OpenShell = %v, want the refusal to say live shells are off and why", err)
	}
}
