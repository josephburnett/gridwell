package server

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/api/tracewire"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// traceServer is a gated web API with a directory to dump into.
func traceServer(t *testing.T) (*http.Client, string, string) {
	t.Helper()
	dir := t.TempDir()
	hs := serveWeb(t, mustNew(t, plugin.NewRegistry(), Config{Dir: dir}))
	return hs.Client(), hs.URL, dir
}

func TestTraceAPIIngestsAndDumps(t *testing.T) {
	cl, base, dumps := traceServer(t)
	marker := "api-test-" + t.Name()
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
	if dir := filepath.Dir(out.Path); dir != config.DumpsDir(dumps) {
		t.Errorf("dump landed in %q, want %q", dir, config.DumpsDir(dumps))
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

func TestTraceAPIKeepsTheGoodLinesBeforeABadOne(t *testing.T) {
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

func TestTraceAPIsRefuseEverythingButPost(t *testing.T) {
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

// A directory the server does not know is a refusal, not a dump beside the binary.
func TestTraceDumpRefusesWithoutAHome(t *testing.T) {
	hs := serveWeb(t, mustNew(t, plugin.NewRegistry(), Config{}))
	res, err := hs.Client().Post(hs.URL+tracewire.DumpPath, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusInternalServerError {
		t.Errorf("POST /trace/dump with no directory = %d, want 500", res.StatusCode)
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

// A send clock the node cannot read is refused by name: a batch placed at a
// guessed time would interleave with the node's records wrongly and say
// nothing. See TestAClientBatchLandsAtItsEmitTimeOnTheNodesClock for the
// clock read right.
func TestTraceAPIRefusesAGarbledSendClock(t *testing.T) {
	cl, base, _ := traceServer(t)
	body := `{"src":"nav","kind":"nav","msg":"garbled clock","ct":7}` + "\n"
	req, _ := http.NewRequest(http.MethodPost, base+tracewire.Path, strings.NewReader(body))
	req.Header.Set(tracewire.ClockHeader, "noon")
	res, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	msg, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(msg), tracewire.ClockHeader) {
		t.Errorf("a garbled clock answered %d %q, want a 400 naming the header", res.StatusCode, msg)
	}
}

// A dump carries the node's server.yaml as it is on disk, after the ring, so a reader of the dump has the routes, connections and
// plugins without asking; a node with no file says so and still dumps.
func TestTraceDumpCarriesServerYAML(t *testing.T) {
	cl, base, dir := traceServer(t)
	dump := func() []tracewire.Record {
		t.Helper()
		res, err := cl.Post(base+tracewire.DumpPath, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out tracewire.DumpResponse
		if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&out) != nil || out.Path == "" {
			t.Fatalf("POST /trace/dump = %d %+v, want 200 with a path", res.StatusCode, out)
		}
		return readDump(t, out.Path)
	}
	tail := func(records []tracewire.Record) []tracewire.Record {
		t.Helper()
		i := len(records)
		for i > 0 && records[i-1].Src == tracewire.SrcConfig {
			i--
		}
		for j, rec := range records[:i] {
			if rec.Src == tracewire.SrcConfig {
				t.Fatalf("config record %d sits among the ring's: %+v", j, rec)
			}
		}
		for _, rec := range records[i:] {
			if rec.Seq != 0 {
				t.Fatalf("a config record took a seq from the ring: %+v", rec)
			}
		}
		return records[i:]
	}

	got := tail(dump())
	if len(got) != 1 || got[0].Kind != tracewire.KindConfigUnread || got[0].Msg == "" {
		t.Errorf("a node with no server.yaml dumped %+v, want one %s record with the reason", got, tracewire.KindConfigUnread)
	}

	yaml := "# Joe's node\nid: k3f9x2a\n\nroutes:\n" +
		strings.Repeat("  - glob: \"https://gitlab.example.com/*\"\n    plugin: p1234567 # ünïcode\n", 40)
	if len(yaml) <= tracewire.MaxMsg {
		t.Fatal("the fixture must be longer than one record's msg")
	}
	if err := os.WriteFile(config.File(dir), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	var joined strings.Builder
	for i, rec := range tail(dump()) {
		if rec.Kind != tracewire.KindConfigChunk || rec.KV["i"] != strconv.Itoa(i) {
			t.Fatalf("config record %d is %+v, want chunk %d", i, rec, i)
		}
		joined.WriteString(rec.Msg)
	}
	if joined.String() != yaml {
		t.Errorf("the chunks join to %d bytes, not the file's %d exactly", joined.Len(), len(yaml))
	}
	if blob, _ := os.ReadFile(config.File(dir)); string(blob) != yaml {
		t.Error("the dump changed server.yaml")
	}
}
