package server

// The content GET: a GET of a place URL, the client's address grammar
// (pane.DecodeURL), that is not a browser's navigation answers what the place
// names, so one reference opens the client in a browser and hands curl the
// bytes: a text's body, a url's address, a page's bytes as the content API
// serves them, a grid or a door's grid as read_grid answers it, any other
// tile as read_tiles does. It sits behind the password gate like every page,
// and, as the content API, outside the build gate: a tool's read names no
// build.

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/client/pane"
	"github.com/josephburnett/gridwell/internal/mcpapi"
)

// placeOrStatic serves the content GET, and everything else as the static
// client does, or not at all on a headless node.
func (s *Server) placeOrStatic(fsys fs.FS) http.Handler {
	static := http.NotFoundHandler()
	if fsys != nil {
		static = s.staticOrSPA(fsys)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if st, ok := contentGET(r); ok {
			s.servePlace(w, r, st)
			return
		}
		static.ServeHTTP(w, r)
	})
}

// contentGET is the place a GET or HEAD names, ok false for a browser's
// navigation, whose Accept leads with text/html, or a path that is no place.
func contentGET(r *http.Request) (pane.URLState, bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return pane.URLState{}, false
	}
	first, _, _ := strings.Cut(r.Header.Get("Accept"), ",")
	if media, _, _ := strings.Cut(first, ";"); strings.EqualFold(strings.TrimSpace(media), "text/html") {
		return pane.URLState{}, false
	}
	raw := r.URL.Path
	if r.URL.RawQuery != "" {
		raw += "?" + r.URL.RawQuery
	}
	st, err := pane.DecodeURL(raw)
	return st, err == nil
}

// servePlace answers the content of the place st names: its leaf tile, or
// with none its anchor grid, home's when it names none.
func (s *Server) servePlace(w http.ResponseWriter, r *http.Request, st pane.URLState) {
	ctx := r.Context()
	rt := newRouter(s)
	read := mcpapi.NewReader(rt, s.cfg.ID)
	id := st.Workspace
	if id == "" && len(st.TileIDs) > 0 {
		ns := s.cfg.ID
		if st.Anchor != "" {
			ns = rpc.NamespaceOf(st.Anchor)
		}
		id = rpc.QualifyID(ns, st.TileIDs[len(st.TileIDs)-1])
	}
	if id == "" {
		g, err := read.Grid(ctx, st.Anchor)
		answerJSON(w, r.URL.Path, g, err)
		return
	}
	resp, err := rt.GetTile(ctx, &pb.GetTileRequest{TileId: id})
	if err != nil {
		refusedWeb(r.URL.Path, httpStatusError(w, err), err.Error(), nil)
		return
	}
	row := resp.GetTile()
	if rpc.IsDoorKind(row.GetKind()) {
		g, err := read.Grid(ctx, row.GetChildGridId())
		answerJSON(w, r.URL.Path, g, err)
		return
	}
	out, err := read.Tile(ctx, id)
	switch {
	case err != nil:
		answerJSON(w, r.URL.Path, nil, err)
	case out.Tile.Dead:
		refusedWeb(r.URL.Path, http.StatusNotFound, "dead link", nil)
		http.Error(w, "dead link", http.StatusNotFound)
	case out.Body != nil:
		w.Header().Set("Content-Type", textType(rpc.ContentID(row)))
		_, _ = w.Write([]byte(*out.Body))
	case out.URL != "":
		w.Header().Set("Content-Type", "text/uri-list")
		_, _ = w.Write([]byte(out.URL + "\r\n"))
	case rpc.KindOf(row.GetKind()).Content == rpc.ContentAddress:
		// A url with no address is a page, whose bytes are the content API's.
		page := r.Clone(ctx)
		page.URL.Path = contentPathPrefix + ContentToken(s.cfg.Password) + "/" + id
		s.contentAPI().ServeHTTP(w, page)
	default:
		answerJSON(w, r.URL.Path, out, nil)
	}
}

// textType is a body's media type: markdown, but a file that is not is
// plain text.
func textType(id string) string {
	if path, file := rpc.FilePath(id); file && !rpc.Markdown(path) {
		return "text/plain; charset=utf-8"
	}
	return "text/markdown; charset=utf-8"
}

// answerJSON writes a read's answer as the MCP API's JSON, or its refusal.
func answerJSON(w http.ResponseWriter, path string, v any, err error) {
	if err != nil {
		refusedWeb(path, http.StatusBadGateway, err.Error(), nil)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
