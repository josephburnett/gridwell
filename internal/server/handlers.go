// Package server exposes Gridwell's JSON-RPC. Every store method
// follows one of three patterns — return a tile, return a void result,
// or return a typed JSON value — so the handlers themselves collapse to
// a single expression per route table entry via the generic helpers
// (handleTile, handleVoid, handleJSONIn) in server.go.
//
// The handlers that don't fit those shapes — bootstrap (no request body,
// custom response) — stay here as one-off methods.
package server

import (
	"net/http"

	"github.com/josephburnett/gridwell/internal/rpc"
)

func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
	id, err := s.store.RootGridID(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	cx, cy, zoom, err := s.store.RootView(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.BootstrapResponse{
		RootGridID: id,
		RootViewCx: cx,
		RootViewCy: cy,
		RootZoom:   zoom,
	})
}
