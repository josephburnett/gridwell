package server

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/api/tracewire"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// traceServer is a gated web door with a home to dump into.
func traceServer(t *testing.T) (*http.Client, string, string) {
	t.Helper()
	home := t.TempDir()
	hs := serveWeb(t, mustNew(t, plugin.NewRegistry(), Config{Home: home}))
	return hs.Client(), hs.URL, home
}

func TestTraceDoorIngestsAndDumps(t *testing.T) {
	cl, base, home := traceServer(t)
	marker := "door-test-" + t.Name()
	body := `{"origin":"client","src":"nav","kind":"nav","msg":"` + marker + `","kv":{"req":"k3f9x2a"},"cid":"c1","ct":7}` + "\n"
	res, err := cl.Post(base+tracewire.Path, "application/x-ndjson", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /trace = %d, want 204", res.StatusCode)
	}

	res, err = cl.Post(base+tracewire.DumpPath, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST /trace/dump = %d, want 200", res.StatusCode)
	}
	var out struct {
		Path    string `json:"path"`
		Records int    `json:"records"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if dir := filepath.Dir(out.Path); dir != config.DumpsDir(home) {
		t.Errorf("dump landed in %q, want %q", dir, config.DumpsDir(home))
	}
	if !strings.HasPrefix(filepath.Base(out.Path), "trace-") || !strings.HasSuffix(out.Path, ".jsonl") {
		t.Errorf("dump name %q is not trace-<stamp>.jsonl", filepath.Base(out.Path))
	}
	fi, err := os.Stat(out.Path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("dump file = %v (%v), want 0600", fi, err)
	}
	records := readDump(t, out.Path)
	if len(records) != out.Records {
		t.Errorf("the reply says %d records, the file holds %d", out.Records, len(records))
	}
	for _, rec := range records {
		if rec.Msg == marker {
			if rec.Origin != tracewire.OriginClient || rec.CID != "c1" || rec.CT != 7 || rec.KV["req"] != "k3f9x2a" {
				t.Errorf("the client's record came back as %+v", rec)
			}
			return
		}
	}
	t.Errorf("the client's record is not in the dump (%d records)", len(records))
}

func TestTraceDoorKeepsTheGoodLinesBeforeABadOne(t *testing.T) {
	cl, base, _ := traceServer(t)
	marker := "kept-" + t.Name()
	body := `{"src":"nav","msg":"` + marker + `"}` + "\nnot json\n"
	res, err := cl.Post(base+tracewire.Path, "application/x-ndjson", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST /trace with a malformed line = %d, want 400", res.StatusCode)
	}
	blob, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(blob), "line 2") {
		t.Errorf("the 400 body %q does not name the line number", blob)
	}
	res2, err := cl.Post(base+tracewire.DumpPath, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	var out struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(res2.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	for _, rec := range readDump(t, out.Path) {
		if rec.Msg == marker {
			return
		}
	}
	t.Error("the good line before the malformed one was dropped")
}

func TestTraceDoorsRefuseEverythingButPost(t *testing.T) {
	cl, base, _ := traceServer(t)
	for _, path := range []string{tracewire.Path, tracewire.DumpPath} {
		res, err := cl.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("GET %s = %d, want 405", path, res.StatusCode)
		}
	}
}

// A home the server does not know is a refusal, not a dump beside the binary.
func TestTraceDumpRefusesWithoutAHome(t *testing.T) {
	hs := serveWeb(t, mustNew(t, plugin.NewRegistry(), Config{}))
	res, err := hs.Client().Post(hs.URL+tracewire.DumpPath, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusInternalServerError {
		t.Errorf("POST /trace/dump with no home = %d, want 500", res.StatusCode)
	}
}

func readDump(t *testing.T, path string) []tracewire.Record {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []tracewire.Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for line := 1; sc.Scan(); line++ {
		var rec tracewire.Record
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("%s line %d: %v", path, line, err)
		}
		out = append(out, rec)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
