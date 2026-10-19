package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/api/tracewire"
	"github.com/josephburnett/gridwell/client/shellwire"
	"github.com/josephburnett/gridwell/internal/mcpapi"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/trace"
)

// refusalOf is the newest webapi/refused record at path whose msg leads
// with msg.
func refusalOf(path, msg string) (tracewire.Record, bool) {
	records := trace.Default().Snapshot()
	for i := len(records) - 1; i >= 0; i-- {
		rec := records[i]
		if rec.Src == "webapi" && rec.Kind == "refused" && rec.KV["path"] == path && strings.HasPrefix(rec.Msg, msg) {
			return rec, true
		}
	}
	return tracewire.Record{}, false
}

// Every request the web API refuses before an rpc span could say so leaves
// a record with its path and status, and no record holds the password, the
// content token or a bearer.
func TestTheWebAPIRecordsEveryRefusalWithoutItsSecret(t *testing.T) {
	const password = "trace-secret-pw"
	srv := mustNew(t, plugin.NewRegistry(), Config{Password: password, Dir: t.TempDir()})
	bare := webAPITest(t, srv.WebHandler())
	authed := withCookie(t, webAPITest(t, srv.WebHandler()), password)
	do := func(c *http.Client, method, u string, body string, hdr map[string]string) {
		t.Helper()
		req, err := http.NewRequest(method, u, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	do(noRedirect, http.MethodPost, bare.URL+authLoginPath, url.Values{"password": {"wrong-" + password}}.Encode(),
		map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	do(noRedirect, http.MethodGet, bare.URL+authLoginPath+"?token=wrong-"+AuthToken(password), "", nil)
	do(bare.Client(), http.MethodPost, bare.URL+mcpapi.Path, "{}", map[string]string{"Authorization": "Bearer wrong-" + password})
	do(bare.Client(), http.MethodGet, bare.URL+contentPathPrefix+"wrong-"+ContentToken(password)+"/n/1/", "", nil)
	do(authed.Client(), http.MethodPost, authed.URL+tracewire.Path, `{"msg":"one"}`+"\n{bad\n", nil)
	do(authed.Client(), http.MethodGet, authed.URL+shellwire.Path, "", nil)

	for _, want := range []struct{ path, msg, status string }{
		{authLoginPath, "wrong password", "401"},
		{authLoginPath, "wrong token", "401"},
		{mcpapi.Path, "password required", "401"},
		{contentPathPrefix + "n/1", "bad content token", "401"},
		{tracewire.Path, "line 2", "400"},
		{shellwire.Path, "", "400"},
	} {
		rec, ok := refusalOf(want.path, want.msg)
		if !ok || rec.KV["status"] != want.status {
			t.Errorf("no webapi/refused at %s %q with status %s (got %+v)", want.path, want.msg, want.status, rec)
		}
	}
	if rec, _ := refusalOf(mcpapi.Path, "password required"); rec.KV["presented"] != "bearer" {
		t.Errorf("the MCP refusal says %q was presented, want bearer", rec.KV["presented"])
	}
	if rec, _ := refusalOf(tracewire.Path, "line 2"); rec.KV["ingested"] != "1" {
		t.Errorf("the trace refusal counts %q ingested, want 1", rec.KV["ingested"])
	}
	for _, rec := range trace.Default().Snapshot() {
		blob, _ := json.Marshal(rec)
		for _, secret := range []string{password, AuthToken(password), ContentToken(password)} {
			if strings.Contains(string(blob), secret) {
				t.Fatalf("a record holds a secret: %s", blob)
			}
		}
	}
}

// A page of another build is refused before its verb, and the refusal is a
// webapi/build record naming both builds.
func TestABuildRefusalIsTraced(t *testing.T) {
	f := gateFixture(t, gateNodeBuild)
	cl, _, _ := f.page("page-traced")
	if _, err := cl.GetGrid(context.Background(), f.root); err == nil {
		t.Fatal("a page of another build was answered")
	}
	for _, rec := range trace.Default().Snapshot() {
		if rec.Src == "webapi" && rec.Kind == "build" && rec.KV["client"] == "page-traced" && rec.KV["node"] == gateNodeBuild {
			return
		}
	}
	t.Error("no webapi/build record names the two builds")
}
