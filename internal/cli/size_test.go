//go:build unix

package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/local/store"
)

// clean refuses while the serve lock is held, the fact `gridwell status`
// reads, and the database is byte-identical after the refusal.
func TestCleanRefusesWhileServing(t *testing.T) {
	dir, _ := backupTestDir(t)
	before, err := os.ReadFile(config.DBFile(dir))
	if err != nil {
		t.Fatal(err)
	}
	l, err := acquireServeLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	l.WriteBanner("gridwell: serving on 127.0.0.1:4242 (static=embedded)")
	var out bytes.Buffer
	if code := clean(&out, dir, true); code != 1 {
		t.Errorf("clean while serving = %d, want 1", code)
	}
	l.Release()
	after, err := os.ReadFile(config.DBFile(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("a refused clean changed gridwell.db")
	}
	if code := clean(&out, dir, true); code != 0 {
		t.Errorf("clean on a stopped node = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "before,") {
		t.Errorf("clean printed no before/after line:\n%s", out.String())
	}
}

// size reads beside a held serve lock and writes nothing.
func TestSizeReadsWhileServing(t *testing.T) {
	dir, _ := backupTestDir(t)
	l, err := acquireServeLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	before, _ := os.ReadFile(config.DBFile(dir))
	var out bytes.Buffer
	if err := sizeOf(&out, dir, false); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(config.DBFile(dir)); !bytes.Equal(before, after) {
		t.Error("size changed gridwell.db")
	}
	for _, want := range []string{"gridwell.db", "cache.db", "disposable", "what nothing reaches", "blobs"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("size report lacks %q:\n%s", want, out.String())
		}
	}
}

// clean reads layouts by the node's id from server.yaml, as serve does: a
// visit a workspace tile's layout names stays, and one nothing names goes.
func TestCleanSweepsTheVisitsNoLayoutNames(t *testing.T) {
	dir, rootID := backupTestDir(t)
	cfg, err := config.Load(config.File(dir))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := store.Open(config.DBFile(dir))
	if err != nil {
		t.Fatal(err)
	}
	held, err := st.CreateScratchURL(ctx, "https://example.org")
	if err != nil {
		t.Fatal(err)
	}
	loose, err := st.CreateScratchURL(ctx, "https://example.net")
	if err != nil {
		t.Fatal(err)
	}
	layout := `{"v":1,"root":{"pane":{"id":"p1","text_focus":"` + cfg.ID + `/` + held.Id + `"}},"focus":"p1"}`
	if _, err := st.CreateWorkspace(ctx, rootID, 0, 0, 1, 1, "ws", false, []byte(layout)); err != nil {
		t.Fatal(err)
	}
	st.Close()

	var out bytes.Buffer
	if code := clean(&out, dir, true); code != 0 {
		t.Fatalf("clean = %d:\n%s", code, out.String())
	}
	st, err = store.Open(config.DBFile(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.GetTile(ctx, held.Id); err != nil {
		t.Errorf("clean swept the visit a layout names: %v", err)
	}
	if _, err := st.GetTile(ctx, loose.Id); err == nil {
		t.Error("clean kept the visit no layout names")
	}
}
