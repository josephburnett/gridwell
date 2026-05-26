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
	writeJSON(w, &rpc.BootstrapResponse{RootGridID: id})
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
	data, mime, err := s.store.GetBlob(r.Context(), req.BlobID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.GetBlobResponse{Data: data, MimeType: mime})
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

func (s *Server) createFile(w http.ResponseWriter, r *http.Request) {
	var req rpc.CreateFileRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	n, err := s.store.CreateFile(r.Context(), &req)
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
	writeJSON(w, &rpc.MoveTileResponse{Tile: *n})
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

func (s *Server) setTileViewport(w http.ResponseWriter, r *http.Request) {
	var req rpc.SetTileViewportRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	n, err := s.store.SetTileViewport(r.Context(), &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.TileResponse{Tile: *n})
}

func (s *Server) setGridDefaultView(w http.ResponseWriter, r *http.Request) {
	var req rpc.SetGridDefaultViewRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	g, err := s.store.SetGridDefaultView(r.Context(), &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.SetGridDefaultViewResponse{Grid: *g})
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

func (s *Server) updateFileContent(w http.ResponseWriter, r *http.Request) {
	var req rpc.UpdateFileContentRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	n, err := s.store.UpdateFileContent(r.Context(), &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.TileResponse{Tile: *n})
}

func (s *Server) forkURL(w http.ResponseWriter, r *http.Request) {
	var req rpc.ForkURLRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	t, err := s.store.ForkURL(r.Context(), &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.ForkURLResponse{Tile: *t})
}

func (s *Server) ascendAtRoot(w http.ResponseWriter, r *http.Request) {
	resp, err := s.store.AscendAtRoot(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, resp)
}
