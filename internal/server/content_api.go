package server

// The HTTP /content/ API: the node's pages. A GET or HEAD of
//
//	/content/<content-token>/<qualified-tile-id>
//
// becomes one ServeContent RPC routed through servePage, so links resolve and
// transit hops forward exactly like ReadContent, and the answer is the page's
// bytes, or the one byte range a Range header asks (206, or 416 past the
// end), with its media type and length, written as each chunk arrives, so a
// video plays and seeks while it streams and no layer holds a page whole.
// Nothing of the request but the tile and that range rides along: no
// subpath, no query, no other header, no body.
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
	"fmt"
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
		// The first chunk names the media type and the size and starts the
		// answer; a failure before it is the status, and one after it cuts
		// the answer short, which the reader sees as a broken page.
		req := &pb.ServeContentRequest{TileId: tileID}
		ranged := false
		if rng := r.Header.Get("Range"); rng != "" {
			req.Offset, req.Length, ranged = pageRange(rng)
		}
		started, bodyless := false, r.Method == http.MethodHead
		start := func(c *pb.ServeContentChunk) {
			started = true
			h := w.Header()
			h.Set("Content-Security-Policy", contentSandbox)
			h.Set("X-Content-Type-Options", "nosniff")
			if c.GetMediaType() != "" {
				h.Set("Content-Type", c.GetMediaType())
			}
			code := http.StatusOK
			if c.Size != nil {
				size := c.GetSize()
				h.Set("Accept-Ranges", "bytes")
				from, n, ok := rpc.PageRange(req.Offset, req.Length, size)
				switch {
				case ranged && (!ok || n == 0):
					h.Set("Content-Range", "bytes */"+strconv.FormatInt(size, 10))
					code, bodyless, n = http.StatusRequestedRangeNotSatisfiable, true, 0
				case ranged:
					h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, from+n-1, size))
					code = http.StatusPartialContent
				}
				h.Set("Content-Length", strconv.FormatInt(n, 10))
			}
			w.WriteHeader(code)
		}
		err := s.servePage(r.Context(), req, func(chunk *pb.ServeContentChunk) error {
			if !started {
				start(chunk)
			}
			if bodyless {
				return errHeadAnswered
			}
			_, err := w.Write(chunk.GetData())
			return err
		})
		switch {
		case err == nil && !started:
			start(&pb.ServeContentChunk{})
		case err == nil, errors.Is(err, errHeadAnswered):
		case !started:
			refusedWeb(path, httpStatusError(w, err), err.Error(), nil)
		default:
			refusedWeb(path, http.StatusOK, "cut short: "+err.Error(), nil)
			panic(http.ErrAbortHandler)
		}
	})
}

// errHeadAnswered ends a HEAD's page, or a range past its end, at its first
// chunk, whose headers are the whole answer.
var errHeadAnswered = errors.New("a HEAD is answered by its headers")

// pageRange is the one byte range a Range header asks, as a
// ServeContentRequest spells it; ok is false for none it can read, a list of
// ranges included, which is answered whole.
func pageRange(h string) (offset, length int64, ok bool) {
	spec, found := strings.CutPrefix(strings.TrimSpace(h), "bytes=")
	a, b, dash := strings.Cut(spec, "-")
	if !found || !dash || strings.Contains(spec, ",") {
		return 0, 0, false
	}
	end, err := strconv.ParseInt(b, 10, 64)
	if a == "" {
		return -end, 0, err == nil && end > 0
	}
	start, serr := strconv.ParseInt(a, 10, 64)
	switch {
	case serr != nil || start < 0:
		return 0, 0, false
	case b == "":
		return start, 0, true
	case err != nil || end < start:
		return 0, 0, false
	}
	return start, end - start + 1, true
}

// httpStatusError maps a routing or RPC failure onto the API's HTTP surface
// and answers the status it wrote. Unimplemented is deliberately 404: a
// source that serves no page has no pages.
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
