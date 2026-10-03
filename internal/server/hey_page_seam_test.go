package server

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/plugin"
)

// The hey plugin's email page through the shipped binary and the content
// door. The plugin runs the `hey` CLI named by its `binary:` key, so a shell
// script standing in for it is the whole fake; every word in it is made up.
// The page HEY's CLI answers has no stylesheet and keeps its quoted text in a
// figure's JSON attribute; what the door serves must carry the plugin's own
// stylesheet and show that quote, as an HTML document.

func fakeHeyCLI(t *testing.T) string {
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
	script := `#!/bin/sh
case "$1 $2" in
  "box view")
    if [ "$3" = imbox ]; then
      echo '{"ok":true,"data":{"id":1,"kind":"imbox","name":"Imbox","postings":[{"id":900,"topic_id":101,"kind":"topic","name":"Kite day","summary":"windy","created_at":"2026-01-05T14:03:00Z","creator":{"name":"Wren","email_address":"wren@example.com"}}]}}'
    else
      echo '{"ok":true,"data":{"id":2,"kind":"'"$3"'","name":"Other","postings":[]}}'
    fi
    ;;
  "watch --events")
    echo '{"change":"ready","at":"2026-01-05T14:03:00Z"}'
    exec sleep 3600
    ;;
  "thread read")
    cat <<'HTML'
<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Kite day</title></head><body>
<article id="entry-1" data-entry-id="1" data-created-at="2026-01-05T14:03" data-body-state="hydrated"><header>From: Wren — 2026-01-05T14:03</header><div>shall we?</div>` + figure + `</article>
</body></html>
HTML
    ;;
  *)
    echo "Error: unknown command" >&2
    exit 1
    ;;
esac
`
	path := filepath.Join(t.TempDir(), "hey")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHeyPageThroughTheContentDoor(t *testing.T) {
	client := newPluginClient(t, "hey", map[string]string{"binary": fakeHeyCLI(t)})
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
