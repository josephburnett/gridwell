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

	"github.com/josephburnett/gridwell/internal/procsource"
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

func TestDisplayName(t *testing.T) {
	cases := []struct {
		info procsource.Info
		want string
	}{
		{procsource.Info{PID: 200, Name: "bash"}, "bash"},
		{procsource.Info{PID: 300, Name: "", CmdLine: "/usr/bin/firefox --new-instance"}, "firefox"},
		{procsource.Info{PID: 1, Name: "init", CmdLine: "/sbin/init"}, "init"},
		{procsource.Info{PID: 4242}, "pid 4242"},
		{procsource.Info{PID: 4243, Name: "", CmdLine: "/ /"}, "pid 4243"},
		{procsource.Info{PID: 4244, Name: "", CmdLine: " "}, "pid 4244"},
		{procsource.Info{PID: 4245, Name: "", CmdLine: "\t\n"}, "pid 4245"},
	}
	for _, c := range cases {
		if got := displayName(c.info); got != c.want {
			t.Errorf("displayName(%+v) = %q, want %q", c.info, got, c.want)
		}
	}
}

// TestListAuthoritativeWhenParentGone: when the parent process is definitively
// absent from /proc, List must return an authoritative-empty listing so the
// host sweeps all tiles — including reparented children that are still running.
func TestListAuthoritativeWhenParentGone(t *testing.T) {
	root := t.TempDir()
	writeProc(t, root, 100, 1, "parent", "")
	writeProc(t, root, 200, 100, "child", "")
	s := New(root, &recordKiller{})
	ctx := context.Background()

	// First listing: normal non-authoritative with @info + child.
	lst, err := s.List(ctx, "100")
	if err != nil {
		t.Fatal(err)
	}
	if lst.Authoritative {
		t.Error("normal listing should be non-authoritative")
	}
	if len(lst.Nodes) != 2 {
		t.Fatalf("want 2 nodes, got %d", len(lst.Nodes))
	}

	// Parent exits: remove its /proc directory.
	if err := os.RemoveAll(filepath.Join(root, "100")); err != nil {
		t.Fatal(err)
	}

	lst2, err := s.List(ctx, "100")
	if err != nil {
		t.Fatal(err)
	}
	if !lst2.Authoritative {
		t.Error("listing after parent exit must be authoritative so host sweeps tiles")
	}
	if len(lst2.Nodes) != 0 {
		t.Errorf("authoritative-gone listing must be empty, got %d nodes", len(lst2.Nodes))
	}
}

// TestListPreservesWhenParentTransientlyUnreadable: if the parent's /proc
// directory exists but its status file is unreadable (transient), the listing
// must be non-authoritative so the host can probe and keep the @info tile.
func TestListPreservesWhenParentTransientlyUnreadable(t *testing.T) {
	root := t.TempDir()
	writeProc(t, root, 100, 1, "parent", "")
	writeProc(t, root, 200, 100, "child", "")
	s := New(root, &recordKiller{})
	ctx := context.Background()

	// Remove status (dir still exists → not definitively gone).
	if err := os.Remove(filepath.Join(root, "100", "status")); err != nil {
		t.Fatal(err)
	}

	lst, err := s.List(ctx, "100")
	if err != nil {
		t.Fatal(err)
	}
	if lst.Authoritative {
		t.Error("transient failure listing must be non-authoritative so @info tile survives")
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
