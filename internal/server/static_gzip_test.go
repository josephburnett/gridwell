package server

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fmt"
	"github.com/josephburnett/gridwell/web"
)

// The compressed-static class (2026-08-12): any static file with a FRESH
// .gz sidecar serves gzipped to a client that accepts it — the case that
// matters is gridwell.wasm (~33 MB raw, ~8 MB gzipped; a phone on a
// relayed tailscale link reads it every boot, and uncompressed that is
// minutes of blank page). The contract has four sides: negotiation
// (gzip in, identity out when the client can't), byte fidelity (the
// decompressed body is EXACTLY the raw file), type fidelity (Content-Type
// stays the raw extension's — instantiateStreaming refuses anything but
// application/wasm), and freshness (a sidecar older than the raw file is
// ignored — a rebuilt wasm must never be shadowed by last build's gz).

func gzipStaticServer(t *testing.T) (hs *httptest.Server, dir string, raw []byte) {
	t.Helper()
	dir = t.TempDir()
	raw = bytes.Repeat([]byte("gridwell wasm bytes "), 1000)
	var gzBuf bytes.Buffer
	zw := gzip.NewWriter(&gzBuf)
	if _, err := zw.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.wasm"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.wasm.gz"), gzBuf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<title>x</title>"), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := New(nil, Config{StaticFS: os.DirFS(dir)})
	hs = httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return hs, dir, raw
}

func getEncoded(t *testing.T, url, acceptEncoding string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	// DisableCompression so the transport neither adds Accept-Encoding nor
	// transparently gunzips — the test must see the wire truth.
	c := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func TestStaticGzipSidecar(t *testing.T) {
	hs, _, raw := gzipStaticServer(t)

	// A gzip-accepting client gets the sidecar: encoded on the wire, the
	// raw extension's Content-Type, and bytes that decompress to EXACTLY
	// the raw file.
	res := getEncoded(t, hs.URL+"/app.wasm", "gzip, deflate, br")
	if res.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", res.Header.Get("Content-Encoding"))
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/wasm" {
		t.Errorf("Content-Type = %q, want application/wasm (instantiateStreaming refuses anything else)", ct)
	}
	if v := res.Header.Get("Vary"); v != "Accept-Encoding" {
		t.Errorf("Vary = %q, want Accept-Encoding (a shared cache must key on it)", v)
	}
	if us := res.Header.Get("X-Uncompressed-Size"); us != fmt.Sprintf("%d", len(raw)) {
		t.Errorf("X-Uncompressed-Size = %q, want %d (the boot percent counter reads it)", us, len(raw))
	}
	zr, err := gzip.NewReader(res.Body)
	if err != nil {
		t.Fatalf("body is not gzip: %v", err)
	}
	var out bytes.Buffer
	if _, err := out.ReadFrom(zr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), raw) {
		t.Errorf("decompressed body differs from the raw file (%d vs %d bytes)", out.Len(), len(raw))
	}

	// A client that cannot gzip gets identity — the raw bytes, no encoding.
	res = getEncoded(t, hs.URL+"/app.wasm", "")
	if res.Header.Get("Content-Encoding") != "" {
		t.Errorf("identity Content-Encoding = %q, want none", res.Header.Get("Content-Encoding"))
	}
	var plain bytes.Buffer
	if _, err := plain.ReadFrom(res.Body); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plain.Bytes(), raw) {
		t.Errorf("identity body differs from the raw file")
	}

	// A file with no sidecar serves identity even to a gzip client.
	res = getEncoded(t, hs.URL+"/index.html", "gzip")
	if res.Header.Get("Content-Encoding") != "" {
		t.Errorf("no-sidecar Content-Encoding = %q, want none", res.Header.Get("Content-Encoding"))
	}
}

// TestEmbeddedWebClientServes pins self-containedness (2026-08-12): the
// server, handed the EMBEDDED web.FS (the distributed binary's default),
// serves the SPA and the wasm — gzipped when accepted, with the type
// instantiateStreaming requires — with no files on disk at all. This is
// the "copy the binaries to another machine" contract.
func TestEmbeddedWebClientServes(t *testing.T) {
	srv := New(nil, Config{StaticFS: web.FS})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	res := getEncoded(t, hs.URL+"/", "")
	body := new(bytes.Buffer)
	if _, err := body.ReadFrom(res.Body); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || !bytes.Contains(body.Bytes(), []byte("gridwell.wasm")) {
		t.Fatalf("GET / = %d, want the embedded index.html (got %d bytes)", res.StatusCode, body.Len())
	}

	res = getEncoded(t, hs.URL+"/gridwell.wasm", "gzip")
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Encoding") != "gzip" {
		t.Errorf("embedded wasm GET = %d enc=%q, want 200 gzip (the sidecar is embedded too)",
			res.StatusCode, res.Header.Get("Content-Encoding"))
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/wasm" {
		t.Errorf("embedded wasm Content-Type = %q, want application/wasm", ct)
	}

	// The vendored xterm assets ride along — the whole client, one binary.
	if res := getEncoded(t, hs.URL+"/vendor/xterm/xterm.min.js", ""); res.StatusCode != http.StatusOK {
		t.Errorf("embedded vendor asset = %d, want 200", res.StatusCode)
	}
}

// TestStaticGzipStaleSidecarIgnored pins the freshness guard: after the raw
// file is rebuilt, last build's sidecar must be IGNORED, not served — a
// stale shadow would hand every browser an old wasm forever.
func TestStaticGzipStaleSidecarIgnored(t *testing.T) {
	hs, dir, _ := gzipStaticServer(t)

	rebuilt := []byte("rebuilt raw bytes, newer than the sidecar")
	if err := os.WriteFile(filepath.Join(dir, "app.wasm"), rebuilt, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "app.wasm.gz"), old, old); err != nil {
		t.Fatal(err)
	}

	res := getEncoded(t, hs.URL+"/app.wasm", "gzip")
	if res.Header.Get("Content-Encoding") != "" {
		t.Fatalf("stale sidecar was served (Content-Encoding = %q)", res.Header.Get("Content-Encoding"))
	}
	var body bytes.Buffer
	if _, err := body.ReadFrom(res.Body); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body.Bytes(), rebuilt) {
		t.Errorf("body is not the rebuilt file — a stale sidecar shadowed it")
	}
}
