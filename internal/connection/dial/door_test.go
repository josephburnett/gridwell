package dial

// The far side every seam test here dials: the node's own connection door,
// server.ConnectionHandler under server.ConnectionDoorServer, so what a test
// proves about the dialer it proves against what the node serves.

import (
	"net/http"
	"testing"

	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/shellsvc"
	"github.com/josephburnett/gridwell/internal/local/shellsvc/shellsvctest"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

func doorHandler(t *testing.T) http.Handler {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	reg := plugin.NewRegistry()
	reg.Register("ur1", "home", local.New(st, shellsvc.NewManager(shellsvctest.New())), nil)
	return servertest.New(t, reg, server.Config{}).ConnectionHandler()
}
