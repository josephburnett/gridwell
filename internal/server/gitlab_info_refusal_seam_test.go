package server_test

// The shipped gitlab binary over a token GitLab refuses: the node lists it
// broken with the plugin's own sentence, and the same process is healthy with
// its one collection once a working token is written to its token file.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/namespace"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/pluginhost"
	"github.com/josephburnett/gridwell/internal/plugintest"
	"github.com/josephburnett/gridwell/internal/plugintest/gitlabfake"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

func TestGitLabOnARefusedTokenIsBrokenAndHealsWithoutARespawn(t *testing.T) {
	const glUUID = "pglrefuse"
	was := namespace.DefaultBackoff
	t.Cleanup(func() { namespace.DefaultBackoff = was })
	namespace.DefaultBackoff = namespace.Backoff{First: 10 * time.Millisecond, Max: 20 * time.Millisecond}

	gl := gitlabfake.New(t, watchedTodo(1, "2026-08-18T10:00:00Z"))
	cfg := gl.Config(t, nil)
	tokenFile := cfg["token_file"]
	if err := os.WriteFile(tokenFile, []byte("revoked-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "gridwell.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	// Spawned once, here; nothing below spawns again.
	cp := plugintest.Spawn(t, "gitlab", cfg)
	a, stop := pluginhost.Start(cp, st.Namespace(glUUID), nil, "plugin "+glUUID+" watch")
	reg := plugin.NewRegistry()
	reg.Register(glUUID, "gitlab", a, stop)
	hs := servertest.Serve(t, servertest.New(t, reg, server.Config{ID: localNodeID}))
	cl := rpc.NewClient(hs.Client(), hs.URL, connect.WithProtoJSON())

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stream, err := cl.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	row := pluginRow(t, cl, glUUID)
	if !strings.HasPrefix(row.InfoError, "GitLab refused the token in "+tokenFile) || !strings.Contains(row.InfoError, "read_api") {
		t.Errorf("InfoError = %q, want the plugin's sentence naming the token file and the scope", row.InfoError)
	}
	if len(row.MenuEntries) != 0 {
		t.Errorf("a refusing gitlab presents entries: %v", row.MenuEntries)
	}
	if down := recvHealthOf(t, stream, glUUID); down.Healthy {
		t.Fatalf("first health event = %+v, want the refusal as down", down)
	}

	if err := os.WriteFile(tokenFile, []byte(gitlabfake.Token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if up := recvHealthOf(t, stream, glUUID); !up.Healthy {
		t.Fatalf("after the token is fixed, health event = %+v, want up", up)
	}
	row = pluginRow(t, cl, glUUID)
	if row.InfoError != "" || len(row.MenuEntries) != 1 {
		t.Errorf("after the token is fixed the row is %v, want its one collection and no error", row)
	}
}
