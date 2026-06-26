package server

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSessionBlobRoundTrip drives the per-plugin session over the HTTP endpoint
// that the Electron host uses: PUT stores the blob in the owning plugin's DB
// (over GetSession/PutSession streams), GET reads it back. This proves the
// session crosses the Gridwell interface.
func TestSessionBlobRoundTrip(t *testing.T) {
	hs, _, _ := newShellBridgeServer(t)
	url := hs.URL + "/session/" + shellPluginUUID

	// Initially empty.
	got := httpGet(t, hs, url)
	if len(got) != 0 {
		t.Errorf("initial session = %q, want empty", got)
	}

	// PUT a blob (larger than one chunk to exercise streaming).
	blob := bytes.Repeat([]byte("cookie-jar;"), 20000)
	req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(blob))
	resp, err := hs.Client().Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT status = %d, want 204", resp.StatusCode)
	}

	// GET it back.
	got = httpGet(t, hs, url)
	if !bytes.Equal(got, blob) {
		t.Errorf("round-trip blob mismatch: got %d bytes, want %d", len(got), len(blob))
	}
}

func TestSessionBlobUnknownPlugin(t *testing.T) {
	hs, _, _ := newShellBridgeServer(t)
	resp, err := hs.Client().Get(hs.URL + "/session/nope")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func httpGet(t *testing.T, hs *httptest.Server, url string) []byte {
	t.Helper()
	resp, err := hs.Client().Get(url)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return body
}
