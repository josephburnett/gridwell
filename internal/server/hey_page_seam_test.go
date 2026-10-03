package server

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"strings"
	"testing"
	"time"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugin"
	"github.com/josephburnett/gridwell/internal/plugintest/heyfake"
)

// The hey plugin's email page through the shipped binary and the content
// door, over heyfake; every word in the thread is made up. The page HEY's CLI
// answers has no stylesheet and keeps its quoted text in a figure's JSON
// attribute; what the door serves must carry the plugin's own stylesheet and
// show that quote, as an HTML document.

func fakeHeyCLI(t *testing.T) *heyfake.CLI {
	t.Helper()
	payload, err := json.Marshal(map[string]string{
		"contentType": "text/html",
		"data":        "{}",
		"content":     `<shadow-content><template><blockquote class="gmail_quote"><div>the kite flew sideways</div></blockquote></template></shadow-content>`,
	})
	if err != nil {
		t.Fatal(err)
	}
	figure := `<figure data-trix-attachment="` + html.EscapeString(string(payload)) + `"></figure>`
	hey := heyfake.New(t)
	hey.SetBox("imbox", heyfake.Thread{
		TopicID: 101, Subject: "Kite day", Summary: "windy", From: "Wren", Email: "wren@example.com",
		Created: time.Date(2026, 1, 5, 14, 3, 0, 0, time.UTC),
		HTML: `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Kite day</title></head><body>
<article id="entry-1" data-entry-id="1" data-created-at="2026-01-05T14:03" data-body-state="hydrated"><header>From: Wren — 2026-01-05T14:03</header><div>shall we?</div>` + figure + `</article>
</body></html>
`,
	})
	return hey
}

func TestHeyPageThroughTheContentDoor(t *testing.T) {
	client := newPluginClient(t, "hey", fakeHeyCLI(t).Config(nil))
	reg := plugin.NewRegistry()
	reg.Register("uh1", "hey", client, nil)
	srv := mustNew(t, reg, Config{Password: "pw"})
	hs := webDoorTest(t, srv.WebHandler())

	ctx := context.Background()
	info, err := client.Info(ctx, &gridwellv1.InfoRequest{})
	if err != nil {
		t.Fatal(err)
	}
	tileID := ""
	for _, e := range info.MenuEntries {
		g, err := client.GetGrid(ctx, &gridwellv1.GetGridRequest{GridId: e.GridId})
		if err != nil {
			t.Fatalf("collection %q: %v", e.Label, err)
		}
		for _, tl := range g.Tiles {
			if tl.ServesPage {
				tileID = "uh1/" + tl.Id
			}
		}
	}
	if tileID == "" {
		t.Fatal("no thread tile serves a page")
	}

	res, body := get(t, noRedirect(hs), hs.URL+"/content/"+ContentToken("pw")+"/"+tileID+"/", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET page = %d %q", res.StatusCode, body)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	for _, want := range []string{
		"<style>",
		`<details class="hey-quote"><summary>Quoted text</summary><blockquote class="gmail_quote"><div>the kite flew sideways</div></blockquote></details>`,
		"<header>From: Wren — 2026-01-05T14:03</header>",
		`<a href="https://app.hey.com/topics/101">Open in HEY</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "data-trix-attachment") {
		t.Errorf("the figure was served as HEY wrote it:\n%s", body)
	}
}
