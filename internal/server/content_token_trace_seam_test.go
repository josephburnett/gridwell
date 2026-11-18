package server

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/api/tracewire"
	"github.com/josephburnett/gridwell/client/traceevent"
	"github.com/josephburnett/gridwell/internal/trace"
)

// The content token reads any file on the host, and a dump is handed to
// others, so no record reaching a dump carries it, whoever spelled the url:
// the client's live-view record, main's, or the node's own.
func TestADumpHoldsNoContentToken(t *testing.T) {
	cl, base, _ := traceServer(t)
	token := ContentToken(testPassword)
	const tile = "abcdefg/~L2V0Yy9wYXNzd2Q"
	page := rpc.PageURL(base, token, tile)

	open := traceevent.URLOpen("p1", tile, page)
	var body strings.Builder
	for _, rec := range []tracewire.Record{
		{Origin: tracewire.OriginClient, Src: open.Src, Kind: open.Kind, Msg: open.Msg, KV: open.KV},
		// What viewtrace.viewCreated sends: the address as the msg.
		{Origin: tracewire.OriginElectron, Src: "webview", Kind: "create", Msg: page, KV: map[string]string{"view": "p1", "tile": tile}},
	} {
		blob, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		body.Write(blob)
		body.WriteByte('\n')
	}
	res, err := cl.Post(base+tracewire.Path, "application/x-ndjson", strings.NewReader(body.String()))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /trace = %d, want 204", res.StatusCode)
	}
	trace.Emit("test", "log", "fetching "+page, map[string]string{"url": page})

	res, err = cl.Post(base+tracewire.DumpPath, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(out.Path)
	if err != nil {
		t.Fatal(err)
	}
	var held []string
	for _, line := range strings.Split(string(blob), "\n") {
		if strings.Contains(line, token) {
			held = append(held, line)
		}
	}
	if len(held) > 0 {
		t.Fatalf("the dump holds the content token:\n%s", strings.Join(held, "\n"))
	}
	if n := strings.Count(string(blob), rpc.ContentPath+tile); n < 4 {
		t.Errorf("the dump names the page %d times, want 4: the redaction must keep the tile", n)
	}
}
