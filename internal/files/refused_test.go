package files

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/rpc"
)

// A directory the OS refuses this user is a verdict, not darkness: a read
// of it is refused with the reason, its parent is told and its door then
// says why, and the files stay light and told, though a session shows it.
// A listing opens no subdirectory, so the door says nothing before a read
// was refused. Its permission given back, it lists again with nothing told
// but the changes.
func TestARefusedDirectoryIsAVerdictNotDark(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	f := newFixture(t)
	locked := f.mkdir("locked")
	f.write("locked/a.md", "a")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	ev := f.events()
	f.show(f.root)
	if door := tileIn(t, f.grid(f.root), locked); door.Kind != rpc.KindDoor || door.ReadOnly != "" {
		t.Errorf("the door before any read of it = kind %q, read_only %q, want a plain door", door.Kind, door.ReadOnly)
	}
	f.show(f.root, locked)
	_, err := f.s.GetGrid(context.Background(), &pb.GetGridRequest{GridId: idOf(locked)})
	wantCode(t, "GetGrid of the refused directory", err, codes.PermissionDenied)
	if !strings.Contains(err.Error(), "cannot be read") {
		t.Errorf("the refusal = %v, want its reason", err)
	}
	await(t, ev, "the parent told after the refusal", func(e *pb.Event) bool { return e.GetGridChanged().GetGridId() == idOf(f.root) })
	if door := tileIn(t, f.grid(f.root), locked); !strings.Contains(door.ReadOnly, "locked cannot be read") {
		t.Errorf("the refused directory's door says read_only %q, want why", door.ReadOnly)
	}
	if h := f.s.health(); h != (health{}) {
		t.Errorf("health after a refused read = %+v, want light and told", h)
	}
	quiet(t, ev, "a health event for a refused directory", func(e *pb.Event) bool { return e.GetSourceHealth() != nil })

	if err := os.Chmod(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := names(f.grid(locked)); len(got) != 1 || got[0] != "a.md" {
		t.Errorf("the directory given back lists %v", got)
	}
	if door := tileIn(t, f.grid(f.root), locked); door.ReadOnly != "" {
		t.Errorf("the door given back still says %q", door.ReadOnly)
	}
	told := map[string]int{}
	for deadline := time.After(3 * debounceWindow); ; {
		select {
		case e := <-ev:
			if e.GetSourceHealth() != nil {
				t.Errorf("a health event after the permission came back: %v", e)
			}
			if g := e.GetGridChanged(); g != nil {
				told[g.GridId]++
			}
			continue
		case <-deadline:
		}
		break
	}
	for g, n := range told {
		if n > 1 {
			t.Errorf("%s told %d times, want once at most", g, n)
		}
	}
}

// A root the OS refuses is refused the same, and not dark.
func TestARefusedRootIsAVerdictNotDark(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	f := newFixture(t)
	if err := os.Chmod(f.root, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.root, 0o755) })
	f.show(f.root)
	_, err := f.s.GetGrid(context.Background(), &pb.GetGridRequest{GridId: idOf(f.root)})
	wantCode(t, "GetGrid of the refused root", err, codes.PermissionDenied)
	if h := f.s.health(); h != (health{}) {
		t.Errorf("health = %+v, want light and told", h)
	}
}

// A recursive find passes over a directory the OS refuses, as over a gone
// one: it holds nothing to find.
func TestARecursiveFindPassesARefusedDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	f := newFixture(t)
	f.write("a.md", "a")
	locked := f.mkdir("locked")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if got := f.find(&pb.FindRequest{GridId: idOf(f.root), Recurse: true, Kind: rpc.KindText}); len(got) != 1 {
		t.Errorf("a find past the refused directory = %v, want the one text", got)
	}
}
