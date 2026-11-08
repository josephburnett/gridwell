package server

// The HTTP /content/ API: the node's pages. A GET or HEAD of
//
//	/content/<content-token>/<qualified-tile-id>
//
// becomes one ServeContent RPC routed through servePage, so links resolve and
// transit hops forward exactly like ReadContent, and the answer is the page's
// whole bytes with its media type, written as each chunk arrives, so a video
// plays while it streams and no layer holds a page whole. Nothing of the
// request but the tile rides along: no subpath, no query, no header, no body.
//
// Every response carries the contentSandbox CSP, so the page runs with an
// opaque origin: no cookies, no storage, no reach into the RPC surface.
//
// A sandboxed page and the desktop's native views cannot present the auth
// cookie, so the API is exempt from that gate and carries its own capability
// in the path. Leaked, that token opens this API alone, which reaches only
// the node's pages; changed, every old content URL dies with it.

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"

	gcodes "google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/trace"
)

const contentPathPrefix = rpc.ContentPath

// contentSandbox is the CSP every API response carries. allow-popups lets a
// target=_blank link reach the desktop's window-open handler, which denies the
// popup and opens the address below as a fresh view; no sandboxed window is
// ever created, so nothing needs allow-popups-to-escape-sandbox.
const contentSandbox = "sandbox allow-scripts allow-popups"

// ContentToken is the one derivation of the API's path capability. Its domain
// prefix differs from AuthToken's, so neither token replays as the other.
func ContentToken(password string) string {
	sum := sha256.Sum256([]byte("gridwell-content-v1\n" + password))
	return hex.EncodeToString(sum[:])
}

// parseContentPath splits the token from the qualified tile id, which ends
// at its first tile segment: anything after it is a subpath, and there is
// none.
func parseContentPath(path string) (token, tileID string, ok bool) {
	rest, found := strings.CutPrefix(path, contentPathPrefix)
	if !found {
		return "", "", false
	}
	token, tileID, found = strings.Cut(rest, "/")
	segs := strings.Split(tileID, "/")
	if !found || len(segs) < 2 || !rpc.IsTileSegment(segs[len(segs)-1]) {
		return "", "", false
	}
	for _, seg := range segs[:len(segs)-1] {
		if rpc.IsTileSegment(seg) {
			return "", "", false
		}
	}
	return token, tileID, true
}

// contentAPI is the handler mounted at /content/.
func (s *Server) contentAPI() http.Handler {
	want := []byte(ContentToken(s.cfg.Password))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			refusedWeb(contentPathPrefix, http.StatusMethodNotAllowed, "method "+r.Method, nil)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		token, tileID, ok := parseContentPath(r.URL.Path)
		// The path holds the token, so a record names the tile alone.
		path := contentPathPrefix + tileID
		if !ok {
			refusedWeb(contentPathPrefix, http.StatusNotFound, "not a content path", nil)
			http.NotFound(w, r)
			return
		}
		if subtle.ConstantTimeCompare([]byte(token), want) != 1 {
			// The same shape as a bad cookie elsewhere: a 401, never a
			// hint.
			refusedWeb(path, http.StatusUnauthorized, "bad content token", nil)
			http.Error(w, "gridwell: bad content token", http.StatusUnauthorized)
			return
		}
		// The first chunk names the media type and starts the answer; a
		// failure before it is the status, and one after it cuts the answer
		// short, which the reader sees as a broken page.
		started := false
		start := func(mediaType string) {
			started = true
			h := w.Header()
			h.Set("Content-Security-Policy", contentSandbox)
			h.Set("X-Content-Type-Options", "nosniff")
			if mediaType != "" {
				h.Set("Content-Type", mediaType)
			}
			w.WriteHeader(http.StatusOK)
		}
		err := s.servePage(r.Context(), tileID, func(chunk *pb.ServeContentChunk) error {
			if !started {
				start(chunk.GetMediaType())
			}
			if r.Method == http.MethodHead {
				return errHeadAnswered
			}
			_, err := w.Write(chunk.GetData())
			return err
		})
		switch {
		case err == nil && !started:
			start("")
		case err == nil, errors.Is(err, errHeadAnswered):
		case !started:
			refusedWeb(path, httpStatusError(w, err), err.Error(), nil)
		default:
			refusedWeb(path, http.StatusOK, "cut short: "+err.Error(), nil)
			panic(http.ErrAbortHandler)
		}
	})
}

// errHeadAnswered ends a HEAD's page at its first chunk, whose media type
// is all a HEAD answers.
var errHeadAnswered = errors.New("a HEAD is answered by its headers")

// httpStatusError maps a routing or RPC failure onto the API's HTTP surface
// and answers the status it wrote. Unimplemented is deliberately 404: a
// plugin that serves no web content has no pages.
func httpStatusError(w http.ResponseWriter, err error) int {
	st, _ := status.FromError(err)
	switch st.Code() {
	case gcodes.NotFound, gcodes.Unimplemented, gcodes.InvalidArgument:
		http.Error(w, "not found", http.StatusNotFound)
		return http.StatusNotFound
	case gcodes.PermissionDenied:
		http.Error(w, "forbidden", http.StatusForbidden)
		return http.StatusForbidden
	default:
		http.Error(w, "content API: "+st.Message(), http.StatusBadGateway)
		return http.StatusBadGateway
	}
}

// refusedWeb records a request the web API refused before any rpc span
// could: path, never a token, and the status written.
func refusedWeb(path string, code int, why string, kv map[string]string) {
	if kv == nil {
		kv = map[string]string{}
	}
	kv["path"], kv["status"] = path, strconv.Itoa(code)
	trace.Emit("webapi", "refused", why, kv)
}
