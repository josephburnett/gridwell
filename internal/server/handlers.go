package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/josephburnett/ascent/internal/rpc"
	"github.com/josephburnett/ascent/internal/store"
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
	writeJSON(w, &rpc.NodeResponse{Node: *n})
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
	writeJSON(w, &rpc.NodeResponse{Node: *n})
}

func (s *Server) moveNode(w http.ResponseWriter, r *http.Request) {
	uid, ok := uidOrError(w, r)
	if !ok {
		return
	}
	var req rpc.MoveNodeRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	n, err := s.store.MoveNode(r.Context(), uid, &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.MoveNodeResponse{Node: *n})
}

func (s *Server) cloneNode(w http.ResponseWriter, r *http.Request) {
	uid, ok := uidOrError(w, r)
	if !ok {
		return
	}
	var req rpc.CloneNodeRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	n, err := s.store.CloneNode(r.Context(), uid, &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.NodeResponse{Node: *n})
}

func (s *Server) resizeNode(w http.ResponseWriter, r *http.Request) {
	uid, ok := uidOrError(w, r)
	if !ok {
		return
	}
	var req rpc.ResizeNodeRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	n, err := s.store.ResizeNode(r.Context(), uid, &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.NodeResponse{Node: *n})
}

func (s *Server) setNodeViewport(w http.ResponseWriter, r *http.Request) {
	uid, ok := uidOrError(w, r)
	if !ok {
		return
	}
	var req rpc.SetNodeViewportRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, err)
		return
	}
	n, err := s.store.SetNodeViewport(r.Context(), uid, &req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, &rpc.NodeResponse{Node: *n})
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
	writeJSON(w, &rpc.NodeResponse{Node: *n})
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
	writeJSON(w, &rpc.NodeResponse{Node: *n})
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
	writeJSON(w, &rpc.NodeResponse{Node: *n})
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
