package server_test

// The content API answers one byte range of a page, as Safari's and
// AVFoundation's players need to play and seek a video: 206 with its range,
// 416 past the end, and 200 with the whole length when no range is asked,
// over the files here and over a connection alike.

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/config"
)

// pageGet asks url for a page with the method and Range header, and answers
// the status, the headers and the body.
func pageGet(t *testing.T, method, url, rng string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, body
}

func checkRanges(t *testing.T, url string, page []byte) {
	t.Helper()
	size := strconv.Itoa(len(page))
	code, h, body := pageGet(t, http.MethodGet, url, "")
	if code != http.StatusOK || h.Get("Content-Length") != size || h.Get("Accept-Ranges") != "bytes" || !bytes.Equal(body, page) {
		t.Errorf("GET = %d length %q ranges %q, %d bytes; want 200, %s, bytes and the page", code, h.Get("Content-Length"), h.Get("Accept-Ranges"), len(body), size)
	}
	for rng, want := range map[string][2]int{"bytes=0-3": {0, 4}, "bytes=100-": {100, len(page)}, "bytes=-10": {len(page) - 10, len(page)}, "bytes=5-9999999": {5, len(page)}} {
		code, h, body := pageGet(t, http.MethodGet, url, rng)
		from, to := want[0], want[1]
		cr := "bytes " + strconv.Itoa(from) + "-" + strconv.Itoa(to-1) + "/" + size
		if code != http.StatusPartialContent || h.Get("Content-Range") != cr || h.Get("Content-Length") != strconv.Itoa(to-from) || !bytes.Equal(body, page[from:to]) {
			t.Errorf("GET %s = %d range %q length %q, %d bytes; want 206 %s and its %d bytes", rng, code, h.Get("Content-Range"), h.Get("Content-Length"), len(body), cr, to-from)
		}
	}
	if code, h, _ := pageGet(t, http.MethodGet, url, "bytes="+size+"-"); code != http.StatusRequestedRangeNotSatisfiable || h.Get("Content-Range") != "bytes */"+size {
		t.Errorf("GET past the end = %d range %q; want 416 bytes */%s", code, h.Get("Content-Range"), size)
	}
	if code, _, body := pageGet(t, http.MethodGet, url, "bytes=0-1,4-5"); code != http.StatusOK || !bytes.Equal(body, page) {
		t.Errorf("GET of two ranges = %d, %d bytes; want the page whole", code, len(body))
	}
	if code, h, body := pageGet(t, http.MethodHead, url, ""); code != http.StatusOK || h.Get("Content-Length") != size || len(body) != 0 {
		t.Errorf("HEAD = %d length %q, %d bytes; want 200, %s and no bytes", code, h.Get("Content-Length"), len(body), size)
	}
}

func TestFilesPageAnswersARange(t *testing.T) {
	root := t.TempDir()
	clip := filepath.Join(root, "clip.mp4")
	page := bytes.Repeat([]byte("0123456789abcdef"), 1<<14)
	if err := os.WriteFile(clip, page, 0o644); err != nil {
		t.Fatal(err)
	}
	n := newFilesNode(t, root)
	lp, err := n.cl.Handshake(n.ctx)
	if err != nil {
		t.Fatal(err)
	}
	checkRanges(t, rpc.PageURL(n.url, lp.ContentToken, fileID(clip)), page)
}

func TestFarPageAnswersARange(t *testing.T) {
	dir := t.TempDir()
	clip := filepath.Join(dir, "clip.mp4")
	page := bytes.Repeat([]byte("fedcba9876543210"), 1<<14)
	if err := os.WriteFile(clip, page, 0o644); err != nil {
		t.Fatal(err)
	}
	h := newTransportHarness(t, []config.ConnectionConfig{{Name: "geneva", Addr: "/s"}}, nil, filesAt(t, "rnode1", dir))
	lp, err := h.localCl.Handshake(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.localCl.GetGrid(t.Context(), farFileID(dir)); err != nil {
		t.Fatal(err)
	}
	checkRanges(t, rpc.PageURL(h.localURL, lp.ContentToken, farFileID(clip)), page)
}
