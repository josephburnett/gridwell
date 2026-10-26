package server

// The HTTP /content/ API: plugins serving web content. A GET, HEAD or POST of
//
//	/content/<content-token>/<qualified-tile-id>/<subpath>
//
// becomes one ServeContent RPC routed through servePage, so links resolve
// and transit hops forward exactly like ReadContent; it carries the method,
// query, headers and body, and each answered chunk is flushed to the page as
// it comes. The API owns the URL grammar, the token gate and the headers in
// nodeHeaders; the plugin owns what the bytes mean and every other header.
//
// Every response carries the contentSandbox CSP, so the page runs with an
// opaque origin: no cookies, no storage, no reach into the RPC surface.
//
// A sandboxed page and the desktop's native views cannot present the auth
// cookie, so the API is exempt from that gate and carries its own capability
// in the path, where relative subresource URLs inherit it. Leaked, that token
// opens this API alone, which reaches only what plugins serve as pages;
// changed, every old content URL dies with it.

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"io"
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

// contentBodyCap bounds a request body, which rides one message: under the
// 4 MiB gRPC message default of every hop that carries it.
const contentBodyCap = 3 << 20

// nodeHeaders are the response headers the node writes and a plugin's
// answer never does: the sandbox and nosniff hold the page, the chunk's
// media_type is the content type, a cookie would land on the node's own
// origin beside its credential, and the node frames the stream.
var nodeHeaders = map[string]bool{
	"Content-Security-Policy": true, "X-Content-Type-Options": true, "Content-Type": true,
	"Set-Cookie": true, "Content-Length": true, "Transfer-Encoding": true, "Connection": true,
}

// credentialHeaders are the request headers that carry the web API's
// credentials, which never reach a plugin.
var credentialHeaders = map[string]bool{"Cookie": true, "Authorization": true}

// ContentToken is the one derivation of the API's path capability. Its domain
// prefix differs from AuthToken's, so neither token replays as the other.
func ContentToken(password string) string {
	sum := sha256.Sum256([]byte("gridwell-content-v1\n" + password))
	return hex.EncodeToString(sum[:])
}

// parseContentPath leans on the id shape: the first tile segment terminates the
// qualified id and everything after it is the page-relative subpath, so a tile
// named by its key serves through the same API as a minted one. needSlash
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

// contentAPI is the handler mounted at /content/.
func (s *Server) contentAPI() http.Handler {
	want := []byte(ContentToken(s.cfg.Password))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
			refusedWeb(contentPathPrefix, http.StatusMethodNotAllowed, "method "+r.Method, nil)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		token, tileID, subpath, needSlash, ok := parseContentPath(r.URL.Path)
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
		if needSlash {
			http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, contentBodyCap))
		if err != nil {
			refusedWeb(path, http.StatusRequestEntityTooLarge, err.Error(), nil)
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		req := &pb.ServeContentRequest{TileId: tileID, Subpath: subpath, Method: r.Method, Query: r.URL.RawQuery, Body: body}
		for name, values := range r.Header {
			if credentialHeaders[name] {
				continue
			}
			for _, v := range values {
				req.Headers = append(req.Headers, &pb.Header{Name: name, Value: v})
			}
		}
		rc := http.NewResponseController(w)
		// The first chunk carries the status, media type and headers, so they
		// are written from inside the stream: a failure before it is an HTTP
		// status, one after it can only truncate the body.
		wroteHeader := false
		serr := s.servePage(r.Context(), req, func(chunk *pb.ServeContentChunk) error {
			if !wroteHeader {
				wroteHeader = true
				writeContentHeaders(w.Header(), chunk, path)
				code := int(chunk.GetStatus())
				if code == 0 {
					code = http.StatusOK
				}
				w.WriteHeader(code)
			}
			if _, err := w.Write(chunk.GetData()); err != nil {
				return err
			}
			return rc.Flush()
		})
		if serr != nil && !wroteHeader {
			refusedWeb(path, httpStatusError(w, serr), serr.Error(), nil)
		}
	})
}

// writeContentHeaders writes the node's headers and then the plugin's, and
// a plugin header that names one of nodeHeaders is dropped with a record.
func writeContentHeaders(h http.Header, chunk *pb.ServeContentChunk, path string) {
	h.Set("Content-Security-Policy", contentSandbox)
	h.Set("X-Content-Type-Options", "nosniff")
	if mt := chunk.GetMediaType(); mt != "" {
		h.Set("Content-Type", mt)
	}
	for _, ph := range chunk.GetHeaders() {
		name := http.CanonicalHeaderKey(ph.GetName())
		if nodeHeaders[name] {
			trace.Emit("webapi", "dropped", "a plugin header the node owns", map[string]string{"path": path, "header": name})
			continue
		}
		h.Add(name, ph.GetValue())
	}
}

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
