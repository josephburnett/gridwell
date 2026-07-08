package server

import (
	"io"
	"net/http"
	"strings"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/rpc"
)

// sessionBlob serves the per-plugin Chromium session blob (cookies + web
// storage) so the Electron host can hydrate a partition on enter and dehydrate
// it on ascent — the plugin is the session boundary.
//
//	GET /session/<plugin-chain>   → the blob bytes (empty if none yet)
//	PUT /session/<plugin-chain>   ← the blob bytes to store
//
// The plugin-chain is the grid NAMESPACE the session belongs to: a bare uuid
// for a local plugin, "<ssh>/<remote-plugin>" (any depth) through a node
// mount — the plugin is the session boundary even when it lives two hops
// away. Routing peels one segment per hop, like every other id: this handler
// resolves the FIRST segment and forwards the REST in the request
// (GetSessionRequest.root_grid_id); the node export applies the same rule on
// the far side, so a slash-less rest means "this plugin".
func (s *Server) sessionBlob(w http.ResponseWriter, r *http.Request) {
	chain := strings.TrimPrefix(r.URL.Path, "/session/")
	if chain == "" {
		http.Error(w, "missing plugin chain", http.StatusBadRequest)
		return
	}
	uuid, rest, qualified := rpc.SplitID(chain)
	if !qualified {
		uuid, rest = chain, ""
	}
	client, ok := s.routeClient(uuid)
	if !ok {
		http.Error(w, "no such plugin", http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		stream, err := client.GetSession(r.Context(), &pb.GetSessionRequest{RootGridId: rest})
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		for {
			chunk, err := stream.Recv()
			if err == io.EOF {
				return
			}
			if err != nil {
				// Headers may already be sent; nothing better than to stop.
				return
			}
			if _, err := w.Write(chunk.Data); err != nil {
				return
			}
		}

	case http.MethodPut:
		stream, err := client.PutSession(r.Context())
		if err != nil {
			writeHTTPError(w, err)
			return
		}
		// Bind the stream up front (works for an empty body too): the rest of
		// the chain rides in root_grid_id and is peeled one segment per hop;
		// "" means "this plugin" (advisory for localdb).
		if err := stream.Send(&pb.PutSessionRequest{RootGridId: rest}); err != nil {
			writeHTTPError(w, err)
			return
		}
		buf := make([]byte, sessionPutChunk)
		for {
			n, rerr := r.Body.Read(buf)
			if n > 0 {
				msg := &pb.PutSessionRequest{Data: append([]byte(nil), buf[:n]...)}
				if err := stream.Send(msg); err != nil {
					writeHTTPError(w, err)
					return
				}
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				http.Error(w, "read body: "+rerr.Error(), http.StatusBadRequest)
				return
			}
		}
		if _, err := stream.CloseAndRecv(); err != nil {
			writeHTTPError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

const sessionPutChunk = 64 * 1024
