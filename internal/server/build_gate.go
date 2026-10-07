package server

// The build gate: a page the node served runs the node's build. Every call a
// page makes names its build (tracewire.BuildHeader, or BuildQuery where a
// header cannot ride), and one naming another build, or none, is refused
// before the verb runs, so an old client never writes to a new node. It runs
// inside the cookie gate and judges only a page's calls, which need no flag:
// a page the node served is same-origin, so its calls carry an Origin naming
// this door, and a cross-origin call never gets past the cookie. A caller
// with no Origin is a tool or a harness, not a page. An unstamped node names
// no build and judges nothing.

import (
	"net/http"
	"net/url"
	"strings"

	"connectrpc.com/connect"
	"github.com/coder/websocket"

	"github.com/josephburnett/gridwell/api/gen/gridwell/v1/gridwellv1connect"
	"github.com/josephburnett/gridwell/api/gwerr"
	"github.com/josephburnett/gridwell/api/tracewire"
	"github.com/josephburnett/gridwell/client/shellwire"
	"github.com/josephburnett/gridwell/internal/trace"
)

// staleBuild answers the build a page's call named when it is not this
// node's. The static files, the trace door and the content door are not
// calls: a stale page must load the new one, and its records describe the
// mismatch.
func (s *Server) staleBuild(r *http.Request) (client string, stale bool) {
	if s.build == "" || !isCall(r.URL.Path) || !fromThisDoor(r) {
		return "", false
	}
	client = r.Header.Get(tracewire.BuildHeader)
	if client == "" {
		client = r.URL.Query().Get(tracewire.BuildQuery)
	}
	return client, client != s.build
}

func isCall(path string) bool {
	return strings.HasPrefix(path, "/"+gridwellv1connect.GridwellName+"/") || path == shellwire.Path
}

func fromThisDoor(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return false
	}
	u, err := url.Parse(o)
	return err == nil && u.Host == r.Host
}

// refuseStaleBuild writes the one verdict in the form the caller reads: a
// Connect error for an RPC or a beacon, an exit frame for the shell socket,
// whose browser end cannot read an HTTP status.
func (s *Server) refuseStaleBuild(w http.ResponseWriter, r *http.Request, client string) {
	verdict := gwerr.StaleBuild(s.build, client)
	trace.Emit("webdoor", "build", "refused: "+verdict.Message(),
		map[string]string{"node": s.build, "client": client, "path": r.URL.Path})
	if r.URL.Path == shellwire.Path {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		writeShellExit(conn, s.shellWriteTimeout, shellwire.EncodeStaleBuild(verdict.Message(), s.build))
		return
	}
	_ = connect.NewErrorWriter().Write(w, r, verdict)
}
