package proc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/josephburnett/gridwell/internal/source"
)

// writeProc creates a stub /proc/<pid> entry (status + cmdline) so tests
// don't depend on the host process table.
func writeProc(t *testing.T, root string, pid, ppid int64, name, cmdline string) {
	t.Helper()
	dir := filepath.Join(root, strconv.FormatInt(pid, 10))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	status := fmt.Sprintf("Name:\t%s\nState:\tS (sleeping)\nPPid:\t%d\nUid:\t1000\t1000\t1000\t1000\nThreads:\t1\n", name, ppid)
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte(status), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := strings.ReplaceAll(cmdline, " ", "\x00")
	if err := os.WriteFile(filepath.Join(dir, "cmdline"), []byte(cmd), 0o644); err != nil {
		t.Fatal(err)
	}
}

// recordKiller records signals instead of sending them.
type recordKiller struct{ calls []string }

func (k *recordKiller) Kill(pid int64, sig syscall.Signal) error {
	k.calls = append(k.calls, fmt.Sprintf("%d:%d", pid, sig))
	return nil
}

func stubTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeProc(t, root, 100, 1, "parent", "/usr/bin/parent")
	writeProc(t, root, 200, 100, "childA", "/bin/childA")
	writeProc(t, root, 201, 100, "childB", "")
	writeProc(t, root, 300, 1, "unrelated", "/bin/other")
	return root
}

func TestAttach(t *testing.T) {
	s := New(t.TempDir(), &recordKiller{})
	att, err := s.Attach(context.Background(), map[string]string{"pid": "1"})
	if err != nil {
		t.Fatal(err)
	}
	if att.RootSourceID != "1" || att.Label != "processes" {
		t.Errorf("root=%q label=%q, want 1/processes", att.RootSourceID, att.Label)
	}
	def, _ := s.Attach(context.Background(), map[string]string{})
	if def.RootSourceID != "1" {
		t.Errorf("default pid = %q, want 1", def.RootSourceID)
	}
	if _, err := s.Attach(context.Background(), map[string]string{"pid": "x"}); err == nil {
		t.Error("bad pid should error")
	}
}

func TestListInfoPlusChildren(t *testing.T) {
	root := stubTree(t)
	s := New(root, &recordKiller{})
	lst, err := s.List(context.Background(), "100")
	if err != nil {
		t.Fatal(err)
	}
	if lst.Authoritative {
		t.Error("proc listing must be non-authoritative")
	}
	byKey := map[string]source.Node{}
	for _, n := range lst.Nodes {
		byKey[n.Key] = n
	}
	// @info + two children (200, 201); 300 is not a child of 100.
	if len(byKey) != 3 {
		t.Fatalf("got %d nodes, want 3: %+v", len(byKey), lst.Nodes)
	}

	info, ok := byKey[infoKey]
	if !ok || info.Kind != source.KindText || info.Body == nil {
		t.Fatalf("@info: %+v", info)
	}
	if info.Body.BlobRef != "100" {
		t.Errorf("@info BlobRef = %q, want 100", info.Body.BlobRef)
	}

	a, ok := byKey["200"]
	if !ok || a.Kind != source.KindWell || a.Child != "200" {
		t.Fatalf("child 200: %+v", a)
	}
	if a.Label != "childA" {
		t.Errorf("200 label = %q, want childA", a.Label)
	}
	if !a.Caps.Delete {
		t.Error("child should be deletable")
	}
	if _, ok := byKey["300"]; ok {
		t.Error("300 is not a child of 100 and must not appear")
	}
}

func TestProbe(t *testing.T) {
	root := stubTree(t)
	s := New(root, &recordKiller{})
	ctx := context.Background()
	if p, _ := s.Probe(ctx, "100", "200"); p != source.PresencePresent {
		t.Errorf("present child = %v", p)
	}
	if p, _ := s.Probe(ctx, "100", "999"); p != source.PresenceGone {
		t.Errorf("gone child = %v, want gone", p)
	}
	// @info probes the well's own pid.
	if p, _ := s.Probe(ctx, "100", infoKey); p != source.PresencePresent {
		t.Errorf("@info of live parent = %v, want present", p)
	}
	if p, _ := s.Probe(ctx, "999", infoKey); p != source.PresenceGone {
		t.Errorf("@info of dead parent = %v, want gone", p)
	}
}

func TestReadBlobInfo(t *testing.T) {
	root := stubTree(t)
	s := New(root, &recordKiller{})

	lst, _ := s.List(context.Background(), "100")
	var wantHash string
	for _, n := range lst.Nodes {
		if n.Key == infoKey {
			wantHash = n.Body.Hash
		}
	}
	body, err := s.ReadBlob(context.Background(), "100", "100")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "# parent") {
		t.Errorf("body:\n%s", body)
	}
	if got := hashHex(body); got != wantHash {
		t.Errorf("ReadBlob hash != List hash; dedup would break")
	}
}

func TestDeleteSignals(t *testing.T) {
	root := stubTree(t)
	k := &recordKiller{}
	s := New(root, k)

	settled, err := s.Delete(context.Background(), "100", "200", 0)
	if err != nil {
		t.Fatal(err)
	}
	if settled {
		t.Error("proc delete must be unsettled (SIGTERM is best-effort)")
	}
	wantSig := fmt.Sprintf("200:%d", syscall.SIGTERM)
	if len(k.calls) != 1 || k.calls[0] != wantSig {
		t.Errorf("kill calls = %v, want [%s]", k.calls, wantSig)
	}

	// Deleting @info signals the well's own pid.
	if _, err := s.Delete(context.Background(), "100", infoKey, 0); err != nil {
		t.Fatal(err)
	}
	wantParent := fmt.Sprintf("100:%d", syscall.SIGTERM)
	if k.calls[1] != wantParent {
		t.Errorf("@info delete signaled %q, want %q", k.calls[1], wantParent)
	}
}
