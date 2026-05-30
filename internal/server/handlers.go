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

func (s *Server) getGrid(w http.ResponseWriter, r *http.Request) {
	var req rpc.GetGridRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	resp, err := s.store.GetGrid(r.Context(), req.GridID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, resp)
}

func (s *Server) getBlob(w http.ResponseWriter, r *http.Request) {
	var req rpc.GetBlobRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	data, err := s.store.GetBlob(r.Context(), req.BlobID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.GetBlobResponse{Data: data})
}

func (s *Server) getTilePreview(w http.ResponseWriter, r *http.Request) {
	var req rpc.GetTilePreviewRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	jpeg, err := s.store.GetTilePreview(r.Context(), req.TileID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.GetTilePreviewResponse{JPEG: jpeg})
}

func (s *Server) createWell(w http.ResponseWriter, r *http.Request) {
	var req rpc.CreateWellRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	n, err := s.store.CreateWell(r.Context(), &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.TileResponse{Tile: *n})
}

func (s *Server) createText(w http.ResponseWriter, r *http.Request) {
	var req rpc.CreateTextRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	n, err := s.store.CreateText(r.Context(), &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.TileResponse{Tile: *n})
}

func (s *Server) createURL(w http.ResponseWriter, r *http.Request) {
	var req rpc.CreateURLRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	n, err := s.store.CreateURL(r.Context(), &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.TileResponse{Tile: *n})
}

func (s *Server) createBlackHole(w http.ResponseWriter, r *http.Request) {
	var req rpc.CreateBlackHoleRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	n, err := s.store.CreateBlackHole(r.Context(), &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.TileResponse{Tile: *n})
}

func (s *Server) moveTile(w http.ResponseWriter, r *http.Request) {
	var req rpc.MoveTileRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	n, err := s.store.MoveTile(r.Context(), &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.TileResponse{Tile: *n})
}

func (s *Server) cloneTile(w http.ResponseWriter, r *http.Request) {
	var req rpc.CloneTileRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	n, err := s.store.CloneTile(r.Context(), &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.TileResponse{Tile: *n})
}

func (s *Server) resizeTile(w http.ResponseWriter, r *http.Request) {
	var req rpc.ResizeTileRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	n, err := s.store.ResizeTile(r.Context(), &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.TileResponse{Tile: *n})
}

func (s *Server) setWellView(w http.ResponseWriter, r *http.Request) {
	var req rpc.SetWellViewRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	n, err := s.store.SetWellView(r.Context(), &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.TileResponse{Tile: *n})
}

func (s *Server) setTextView(w http.ResponseWriter, r *http.Request) {
	var req rpc.SetTextViewRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	n, err := s.store.SetTextView(r.Context(), &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.TileResponse{Tile: *n})
}

func (s *Server) setRootView(w http.ResponseWriter, r *http.Request) {
	var req rpc.SetRootViewRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.SetRootView(r.Context(), &req); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.SetRootViewResponse{})
}

func (s *Server) updateText(w http.ResponseWriter, r *http.Request) {
	var req rpc.UpdateTextRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	n, err := s.store.UpdateText(r.Context(), &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.TileResponse{Tile: *n})
}

func (s *Server) deleteTile(w http.ResponseWriter, r *http.Request) {
	var req rpc.DeleteTileRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.DeleteTile(r.Context(), &req); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.DeleteTileResponse{})
}
