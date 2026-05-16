package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/josephburnett/gridwell/internal/rpc"
	"github.com/josephburnett/gridwell/internal/store"
)

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req rpc.LoginRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	u, err := s.store.AuthenticateUser(r.Context(), req.Username, req.Password)
	if err != nil {
		writeError(w, err)
		return
	}
	tok, err := s.store.CreateSession(r.Context(), u.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    tok,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.SecureCookie,
		SameSite: http.SameSiteStrictMode,
		Expires:  time.Now().Add(store.SessionTTL),
	})
	writeJSON(w, &rpc.LoginResponse{
		UserID: u.ID, Username: u.Username, RootGridID: u.RootGridID,
	})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookieName); err == nil {
		_ = s.store.DeleteSession(r.Context(), c.Value)
	}
	// Always clear the cookie, even if no session was found.
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookieName, Value: "", Path: "/",
		MaxAge: -1, HttpOnly: true, Secure: s.cfg.SecureCookie,
		SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, &rpc.LogoutResponse{})
}

func (s *Server) whoami(w http.ResponseWriter, r *http.Request) {
	uid, ok := uidOrError(w, r)
	if !ok {
		return
	}
	u, err := s.store.GetUser(r.Context(), uid)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.WhoamiResponse{UserID: u.ID, Username: u.Username, RootGridID: u.RootGridID})
}

func (s *Server) getGrid(w http.ResponseWriter, r *http.Request) {
	uid, ok := uidOrError(w, r)
	if !ok {
		return
	}
	var req rpc.GetGridRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	resp, err := s.store.GetGrid(r.Context(), uid, req.GridID)
	if err != nil {
		// No-read on a grid surfaces as a "locked" placeholder rather than
		// a hard error, but only when the user can see the parent well.
		// At the GetGrid endpoint we don't have parent context, so we
		// return Forbidden and let the client render a locked tile in
		// place of the parent well.
		writeError(w, err)
		return
	}
	// Populate runtime Live field on URL tiles before serializing.
	if s.urlStreamer != nil {
		for i := range resp.Tiles {
			if resp.Tiles[i].IsURL() {
				resp.Tiles[i].Live = s.urlStreamer.IsLive(uid, resp.Tiles[i].ID)
			}
		}
	}
	writeJSON(w, resp)
}

func (s *Server) getBlob(w http.ResponseWriter, r *http.Request) {
	uid, ok := uidOrError(w, r)
	if !ok {
		return
	}
	var req rpc.GetBlobRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	data, mime, err := s.store.GetBlob(r.Context(), uid, req.BlobID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.GetBlobResponse{Data: data, MimeType: mime})
}

func (s *Server) getTilePreview(w http.ResponseWriter, r *http.Request) {
	uid, ok := uidOrError(w, r)
	if !ok {
		return
	}
	var req rpc.GetTilePreviewRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	jpeg, err := s.store.GetTilePreview(r.Context(), uid, req.TileID)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.GetTilePreviewResponse{JPEG: jpeg})
}

func (s *Server) createWell(w http.ResponseWriter, r *http.Request) {
	uid, ok := uidOrError(w, r)
	if !ok {
		return
	}
	var req rpc.CreateWellRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	n, err := s.store.CreateWell(r.Context(), uid, &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.TileResponse{Tile: *n})
}

func (s *Server) createFile(w http.ResponseWriter, r *http.Request) {
	uid, ok := uidOrError(w, r)
	if !ok {
		return
	}
	var req rpc.CreateFileRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	n, err := s.store.CreateFile(r.Context(), uid, &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.TileResponse{Tile: *n})
}

func (s *Server) moveTile(w http.ResponseWriter, r *http.Request) {
	uid, ok := uidOrError(w, r)
	if !ok {
		return
	}
	var req rpc.MoveTileRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	n, err := s.store.MoveTile(r.Context(), uid, &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.MoveTileResponse{Tile: *n})
}

func (s *Server) cloneTile(w http.ResponseWriter, r *http.Request) {
	uid, ok := uidOrError(w, r)
	if !ok {
		return
	}
	var req rpc.CloneTileRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	n, err := s.store.CloneTile(r.Context(), uid, &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.TileResponse{Tile: *n})
}

func (s *Server) resizeTile(w http.ResponseWriter, r *http.Request) {
	uid, ok := uidOrError(w, r)
	if !ok {
		return
	}
	var req rpc.ResizeTileRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	n, err := s.store.ResizeTile(r.Context(), uid, &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.TileResponse{Tile: *n})
}

func (s *Server) setTileViewport(w http.ResponseWriter, r *http.Request) {
	uid, ok := uidOrError(w, r)
	if !ok {
		return
	}
	var req rpc.SetTileViewportRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	n, err := s.store.SetTileViewport(r.Context(), uid, &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.TileResponse{Tile: *n})
}

func (s *Server) setGridDefaultView(w http.ResponseWriter, r *http.Request) {
	uid, ok := uidOrError(w, r)
	if !ok {
		return
	}
	var req rpc.SetGridDefaultViewRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	g, err := s.store.SetGridDefaultView(r.Context(), uid, &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.SetGridDefaultViewResponse{Grid: *g})
}

func (s *Server) capWell(w http.ResponseWriter, r *http.Request) {
	uid, ok := uidOrError(w, r)
	if !ok {
		return
	}
	var req rpc.CapWellRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	n, err := s.store.CapWell(r.Context(), uid, &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.TileResponse{Tile: *n})
}

func (s *Server) redigWell(w http.ResponseWriter, r *http.Request) {
	uid, ok := uidOrError(w, r)
	if !ok {
		return
	}
	var req rpc.RedigWellRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	n, err := s.store.RedigWell(r.Context(), uid, &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.TileResponse{Tile: *n})
}

func (s *Server) fillWell(w http.ResponseWriter, r *http.Request) {
	uid, ok := uidOrError(w, r)
	if !ok {
		return
	}
	var req rpc.FillWellRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := s.store.FillWell(r.Context(), uid, &req); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.FillWellResponse{})
}

func (s *Server) updateFileContent(w http.ResponseWriter, r *http.Request) {
	uid, ok := uidOrError(w, r)
	if !ok {
		return
	}
	var req rpc.UpdateFileContentRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	n, err := s.store.UpdateFileContent(r.Context(), uid, &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.TileResponse{Tile: *n})
}

func (s *Server) wakeURL(w http.ResponseWriter, r *http.Request) {
	uid, ok := uidOrError(w, r)
	if !ok {
		return
	}
	var req rpc.WakeURLRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	t, err := s.store.WakeURL(r.Context(), uid, &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.WakeURLResponse{Tile: *t})
}

func (s *Server) captureURL(w http.ResponseWriter, r *http.Request) {
	uid, ok := uidOrError(w, r)
	if !ok {
		return
	}
	var req rpc.CaptureURLRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	t, err := s.store.CaptureURL(r.Context(), uid, &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.CaptureURLResponse{Tile: *t})
}

func (s *Server) forkURL(w http.ResponseWriter, r *http.Request) {
	uid, ok := uidOrError(w, r)
	if !ok {
		return
	}
	var req rpc.ForkURLRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	t, err := s.store.ForkURL(r.Context(), uid, &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.ForkURLResponse{Tile: *t})
}

func (s *Server) ascendAtRoot(w http.ResponseWriter, r *http.Request) {
	uid, ok := uidOrError(w, r)
	if !ok {
		return
	}
	resp, err := s.store.AscendAtRoot(r.Context(), uid)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, resp)
}

// silence vet; errors is used by sub-files.
var _ = errors.New
