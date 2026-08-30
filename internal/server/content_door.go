package server

// The HTTP /content/ door: plugins serving WEB CONTENT (2026-08-11). A GET of
//
//	/content/<content-token>/<qualified-tile-id>/<subpath>
//
// becomes one ServeContent RPC routed to the tile's owning plugin — through
// contentRoute, so links resolve and transit hops forward exactly like
// ReadContent — and the stream comes back as the HTTP body. The door is a
// pure translator: it owns the URL grammar, the token gate, and the sandbox
// header; the plugin owns everything about what the bytes mean.
//
// Sandbox: every response carries `Content-Security-Policy: sandbox
// allow-scripts`. The page runs with an OPAQUE origin — scripts may run, but
// there are no cookies, no storage, and no reach into the Gridwell RPC
// surface, so plugin-served content can never act on the user's Gridwell.
// The server stamps the header itself; a plugin cannot override it because
// plugins never write HTTP headers at all.
//
// Token: sandboxed pages and the desktop's native views can't present the
// auth cookie (opaque origins send no credentials; the views live on their
// own session partition), so the door is exempt from the cookie gate and
// carries its own capability in the PATH — where relative subresource URLs
// inherit it for free. ContentToken derives from the same config password
// under a different domain prefix: leaked, it opens only this read-only
// door, never the RPC surface; changed, every old content URL dies with it,
// exactly like the cookie.

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"

	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

const contentPathPrefix = "/content/"

// ContentToken derives the /content/ door's path capability from the
// configured password. The ONE derivation, mirroring AuthToken: the door
// checks it, and Handshake hands it to the client (over the
// cookie-authenticated mux, so only a logged-in client ever learns it).
// The domain prefix differs from AuthToken's so neither token can ever be
// replayed as the other.
func ContentToken(password string) string {
	sum := sha256.Sum256([]byte("gridwell-content-v1\n" + password))
	return hex.EncodeToString(sum[:])
}

// parseContentPath splits a /content/ URL path into its parts. The grammar
// leans on the id-shape owner decision (2026-07-25): namespace segments
// (plugin uuids, node ids, ssh connection names) are never purely numeric,
// and a LOCAL tile id always is — so the first all-digits segment terminates
// the qualified id and everything after it is the page-relative subpath.
// needSlash reports a root-page request missing its trailing slash (the
// caller redirects: relative URLs inside the page resolve against the
// directory, so the root page must live at ".../<id>/").
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
		if isAllDigits(segs[i]) {
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
		// ".../<id>" with no trailing slash: a root-page request at the
		// wrong depth for relative resolution.
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

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// contentDoor is the HTTP handler mounted at /content/ (and exempted from
// the cookie gate — the token in the path IS the credential here).
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
			// Same shape as a bad cookie elsewhere: a 401, never a hint.
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
		// The FIRST chunk carries the status and media type, so the
		// headers are written from inside the stream: a failure before it
		// is still an HTTP status the browser can read, and a failure after
		// it can only truncate the body (the headers are gone; there is
		// nothing truthful left to send).
		wroteHeader := false
		serr := c.ServeContent(r.Context(), &pb.ServeContentRequest{TileId: local, Subpath: subpath},
			func(chunk *pb.ServeContentChunk) error {
				if !wroteHeader {
					wroteHeader = true
					h := w.Header()
					// The sandbox is the door's invariant, stamped on EVERY response.
					h.Set("Content-Security-Policy", "sandbox allow-scripts")
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

// httpStatusError maps a routing/RPC failure onto the door's HTTP surface.
// Unimplemented is the deliberate default: a plugin that serves no web
// content simply has no pages, and the door says 404, not 500.
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
