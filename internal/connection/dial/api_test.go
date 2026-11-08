package dial

// The far side every seam test here dials: the node's own connection API,
// server.ConnectionHandler under server.ConnectionAPIServer, so what a test
// proves about the dialer it proves against what the node serves.

import (
	"net/http"
	"testing"

	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/shellsvc"
	"github.com/josephburnett/gridwell/internal/local/shellsvc/shellsvctest"
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/server"
	"github.com/josephburnett/gridwell/internal/server/servertest"
)

func connectionAPIHandler(t *testing.T) http.Handler {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	home := local.New(st, shellsvc.NewManager(shellsvctest.New()))
	return servertest.New(t, &server.Sources{Home: home}, server.Config{ID: "ur1"}).ConnectionHandler()
}
