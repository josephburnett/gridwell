package server_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/api/rpc"
	"github.com/josephburnett/gridwell/internal/plugintest/gitlabfake"
)

// A todo open in a pane shows its new text once its note changes at GitLab,
// its name unchanged: the shipped plugin's glance reads it again and tells
// it as its entry (plugin standard rule 18).
func TestGitLabOpenTodoShowsItsNewText(t *testing.T) {
	td := watchedTodo(1, "2026-08-18T10:00:00Z")
	td.Body = "the first note"
	gl := gitlabfake.New(t, td)
	k := newContentClientOf(t, "pglbody", "gitlab", gl.Config(t, map[string]string{"refresh": "1s"}), nil)
	k.fetchGrid(k.landing)
	week := k.tile("2026-08-17").ChildGridId
	k.fetchGrid(week)
	id := rpc.ContentID(k.tileIn(week, "!1 change"))
	if got := k.body(id); !bytes.Contains(got, []byte("the first note")) {
		t.Fatalf("open todo %q", got)
	}
	if err := k.cl.SetInterest(k.ctx, []string{week}); err != nil {
		t.Fatal(err)
	}
	k.settle()

	td.Body = "the second note"
	gl.Set(td)
	if !k.run(10*time.Second, func() bool { return bytes.Contains(k.body(id), []byte("the second note")) }) {
		t.Fatalf("the open todo still shows %q ten seconds after its note changed at GitLab (rows told moved: %v)", k.body(id), k.told)
	}
}
