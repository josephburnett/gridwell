package server_test

import (
	"bytes"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/api/rpc"
)

// A process's @info open in a pane shows its new state once the process
// stops, over the real /proc: the shipped plugin's poll of the shown pid
// reads @info again and tells it as its entry (plugin standard rule 18).
func TestProcOpenInfoShowsItsNewState(t *testing.T) {
	p := startForker(t)
	k := newContentClientOf(t, "pprocinfo", "proc", map[string]string{"pid": p.pid}, nil)
	k.fetchGrid(k.landing)
	id := rpc.ContentID(k.tile("@info"))
	if got := k.body(id); !bytes.Contains(got, []byte("state: S")) {
		t.Fatalf("open @info %q", got)
	}
	if err := k.cl.SetInterest(k.ctx, []string{k.landing}); err != nil {
		t.Fatal(err)
	}
	k.settle()

	pid, err := strconv.Atoi(p.pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	if !k.run(10*time.Second, func() bool { return bytes.Contains(k.body(id), []byte("state: T")) }) {
		t.Fatalf("the open @info still shows %q ten seconds after its process stopped (rows told moved: %v)", k.body(id), k.told)
	}
}
