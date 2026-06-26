package server

import (
	"io"
	"net/http"
	"strings"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// sessionBlob serves the per-plugin Chromium session blob (cookies + web
// storage) so the Electron host can hydrate a partition on enter and dehydrate
// it on ascent — the plugin is the session boundary.
//
//	GET /session/<plugin-uuid>   → the blob bytes (empty if none yet)
//	PUT /session/<plugin-uuid>   ← the blob bytes to store
//
// The handler routes to the owning plugin's GetSession / PutSession streams, so
// the session crosses the Gridwell interface like everything else (a remote
// plugin proxies it to the remote node).
func (s *Server) sessionBlob(w http.ResponseWriter, r *http.Request) {
	uuid := strings.TrimPrefix(r.URL.Path, "/session/")
	if uuid == "" {
		http.Error(w, "missing plugin uuid", http.StatusBadRequest)
		return
	}
	client, ok := s.pluginReg.Get(uuid)
	if !ok {
		http.Error(w, "no such plugin", http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		stream, err := client.GetSession(r.Context(), &pb.GetSessionRequest{})
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
		buf := make([]byte, sessionPutChunk)
		first := true
		for {
			n, rerr := r.Body.Read(buf)
			if n > 0 {
				msg := &pb.PutSessionRequest{Data: append([]byte(nil), buf[:n]...)}
				if first {
					msg.RootGridId = uuid // binds the stream; advisory for localdb
					first = false
				}
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
