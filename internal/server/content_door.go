package server

// The HTTP /content/ door: plugins serving web content. A GET of
//
//	/content/<content-token>/<qualified-tile-id>/<subpath>
//
// becomes one ServeContent RPC routed through contentRoute, so links resolve
// and transit hops forward exactly like ReadContent. The door owns the URL
// grammar, the token gate and the sandbox header; the plugin owns what the
// bytes mean.
//
// Every response carries the contentSandbox CSP, so the page runs with an opaque origin: no cookies, no storage, no reach into
// the RPC surface. The server stamps the header, and plugins never write HTTP
// headers at all.
//
// A sandboxed page and the desktop's native views cannot present the auth
// cookie, so the door is exempt from that gate and carries its own capability
// in the path, where relative subresource URLs inherit it. Leaked, that token
// opens only this read-only door; changed, every old content URL dies with it.

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"

	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

const contentPathPrefix = "/content/"

// contentSandbox is the CSP every door response carries. allow-popups lets a
// target=_blank link reach the desktop's window-open handler, which denies the
// popup and opens the address below as a fresh view; no sandboxed window is
// ever created, so nothing needs allow-popups-to-escape-sandbox.
const contentSandbox = "sandbox allow-scripts allow-popups"

// ContentToken is the one derivation of the door's path capability. Its domain
// prefix differs from AuthToken's, so neither token replays as the other.
func ContentToken(password string) string {
	sum := sha256.Sum256([]byte("gridwell-content-v1\n" + password))
	return hex.EncodeToString(sum[:])
}

// parseContentPath leans on the id shape: the first tile segment terminates the
// qualified id and everything after it is the page-relative subpath, so a tile
// named by its key serves through the same door as a minted one. needSlash
// reports a root-page request missing its trailing slash, which the caller
// redirects, because relative URLs resolve against the directory.
func parseContentPath(path string) (token, tileID, subpath string, needSlash, ok bool) {
	rest, found := strings.CutPrefix(path, contentPathPrefix)
	if !found {
		return "", "", "", false, false
	}
	segs := strings.Split(rest, "/")
	if len(segs) < 3 { // token + at least one namespace segment + local id
		return "", "", "", false, false
	}
	token = segs[0]
	idEnd := 0
	for i := 1; i < len(segs); i++ {
		if rpc.IsTileSegment(segs[i]) {
			idEnd = i
			break
		}
	}
	if idEnd == 0 {
		return "", "", "", false, false
	}
	tileID = strings.Join(segs[1:idEnd+1], "/")
	tail := segs[idEnd+1:]
	if len(tail) == 0 {
		// ".../<id>" with no trailing slash: a root-page request at the wrong
		// depth for relative resolution.
		return token, tileID, "", true, true
	}
	subpath = strings.Join(tail, "/")
	for _, seg := range tail {
		if seg == ".." {
			return "", "", "", false, false
		}
	}
	return token, tileID, subpath, false, true
}

// contentDoor is the handler mounted at /content/.
func (s *Server) contentDoor() http.Handler {
	want := []byte(ContentToken(s.cfg.Password))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		token, tileID, subpath, needSlash, ok := parseContentPath(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if subtle.ConstantTimeCompare([]byte(token), want) != 1 {
			// The same shape as a bad cookie elsewhere: a 401, never a
			// hint.
			http.Error(w, "gridwell: bad content token", http.StatusUnauthorized)
			return
		}
		if needSlash {
			http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
			return
		}
		c, local, err := s.contentRoute(r.Context(), tileID)
		if err != nil {
			httpStatusError(w, err)
			return
		}
		// The first chunk carries the status and media type, so headers are
		// written from inside the stream: a failure before it is an HTTP
		// status, one after it can only truncate the body.
		wroteHeader := false
		serr := c.ServeContent(r.Context(), &pb.ServeContentRequest{TileId: local, Subpath: subpath},
			func(chunk *pb.ServeContentChunk) error {
				if !wroteHeader {
					wroteHeader = true
					h := w.Header()
					h.Set("Content-Security-Policy", contentSandbox)
					h.Set("X-Content-Type-Options", "nosniff")
					if mt := chunk.GetMediaType(); mt != "" {
						h.Set("Content-Type", mt)
					}
					code := int(chunk.GetStatus())
					if code == 0 {
						code = http.StatusOK
					}
					w.WriteHeader(code)
				}
				_, werr := w.Write(chunk.GetData())
				return werr
			})
		if serr != nil && !wroteHeader {
			httpStatusError(w, serr)
		}
	})
}

// httpStatusError maps a routing or RPC failure onto the door's HTTP surface.
// Unimplemented is deliberately 404: a plugin that serves no web content has
// no pages.
func httpStatusError(w http.ResponseWriter, err error) {
	st, _ := status.FromError(err)
	switch st.Code() {
	case gcodes.NotFound, gcodes.Unimplemented, gcodes.InvalidArgument:
		http.Error(w, "not found", http.StatusNotFound)
	case gcodes.PermissionDenied:
		http.Error(w, "forbidden", http.StatusForbidden)
	default:
		http.Error(w, "content door: "+st.Message(), http.StatusBadGateway)
	}
}
