package server_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A file's row and its read name its bytes by one stamp, carried from the
// plugin through the node to the client, so the client knows a body it holds
// is current without a version: a change on disk moves the row's stamp, the
// body read under the old one ages, and the new read names the new one.
func TestFsRowAndBodyNameTheFilesBytesByOneStamp(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.md")
	if err := os.WriteFile(path, []byte("# before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	k := newContentClient(t, "pfsstamp", root)
	k.fetchGrid(k.landing)
	row := k.tile("notes.md")
	_, _, basis, err := k.cl.ReadContent(k.ctx, row.Id)
	if err != nil {
		t.Fatal(err)
	}
	if row.ContentStamp == "" || basis.Stamp != row.ContentStamp {
		t.Fatalf("row stamp %q, read stamp %q; want one non-empty stamp", row.ContentStamp, basis.Stamp)
	}
	k.body(row.Id)
	if err := k.cl.SetInterest(k.ctx, []string{k.landing}); err != nil {
		t.Fatal(err)
	}
	k.settle()

	later := time.Now().Add(time.Second)
	if err := os.WriteFile(path, []byte("# after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if !k.run(5*time.Second, func() bool { return k.tile("notes.md").ContentStamp != row.ContentStamp }) {
		t.Fatal("the row's stamp did not move after the file changed on disk")
	}
	if got := k.body(row.Id); string(got) != "# after\n" {
		t.Errorf("body %q after the row's stamp moved; want the new bytes", got)
	}
	if b, _ := k.c.SaveBasis(row.Id); b.Stamp != k.tile("notes.md").ContentStamp {
		t.Errorf("the body is filed under stamp %q, the row names %q", b.Stamp, k.tile("notes.md").ContentStamp)
	}
}
