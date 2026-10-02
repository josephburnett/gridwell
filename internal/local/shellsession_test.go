package local_test

import (
	"context"
	"io"
	"slices"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/local"
	"github.com/josephburnett/gridwell/internal/local/shellsvc/shellsvctest"
	"github.com/josephburnett/gridwell/internal/local/tmux"
)

// The namespace side of a shared session: every tile naming a session attaches
// that one session, the tile it was opened from is the one its detach
// relabels, and the session lives until the last row naming it is destroyed.

func cloneShell(t *testing.T, p *local.Plugin, srcID string, x int64) string {
	t.Helper()
	ctx := context.Background()
	src, err := p.GetTile(ctx, &gridwellv1.GetTileRequest{TileId: srcID})
	if err != nil {
		t.Fatal(err)
	}
	c, err := p.CloneTile(ctx, &gridwellv1.CloneTileRequest{TileId: srcID, DestGridId: src.Tile.GridId, X: x})
	if err != nil {
		t.Fatalf("CloneTile: %v", err)
	}
	return c.Tile.Id
}

// attached is one live OpenShell on a tile: detach ends it and returns what
// OpenShell returned.
type attached struct {
	cancel context.CancelFunc
	done   chan error
}

func (a attached) detach(t *testing.T) error {
	t.Helper()
	a.cancel()
	return <-a.done
}

// attach opens tileID's shell and waits for the fake's echo, so the session is
// held when it returns. A refused open returns the refusal instead.
func attach(t *testing.T, p *local.Plugin, tileID string) (attached, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	up := make(chan *gridwellv1.OpenShellRequest, 2)
	up <- &gridwellv1.OpenShellRequest{TileId: tileID, Resize: &gridwellv1.PTYSize{Cols: 80, Rows: 24}}
	up <- &gridwellv1.OpenShellRequest{Data: []byte("x")}
	down := make(chan struct{}, 8)
	done := make(chan error, 1)
	go func() {
		done <- p.OpenShell(ctx,
			func() (*gridwellv1.OpenShellRequest, error) {
				select {
				case msg := <-up:
					return msg, nil
				case <-ctx.Done():
					return nil, io.EOF
				}
			},
			func(*gridwellv1.OpenShellResponse) error {
				down <- struct{}{}
				return nil
			})
	}()
	select {
	case <-down:
		return attached{cancel, done}, nil
	case err := <-done:
		cancel()
		return attached{}, err
	}
}

// A clone of a never-opened shell shares the session not yet created: opened
// first, it starts the session under its source's key, and the source then
// attaches to that same session.
func TestAClonedShellStartsAndSharesItsSourcesSession(t *testing.T) {
	fake := shellsvctest.New()
	fake.PaneCmd = "top"
	p, a := shellPluginWithTile(t, fake)
	b := cloneShell(t, p, a, 2)

	sb, err := attach(t, p, b)
	if err != nil {
		t.Fatalf("open the clone: %v", err)
	}
	if s := fake.LastSession(); s.Key != a || s.OpenMode != tmux.ModeCreate {
		t.Fatalf("the clone opened session %q mode %v, want its source's %q created", s.Key, s.OpenMode, a)
	}
	if err := sb.detach(t); err != nil {
		t.Fatal(err)
	}
	if got := altOf(t, p, b); got != "top" {
		t.Errorf("the clone's label = %q, want the capture %q", got, "top")
	}
	if got := altOf(t, p, a); got != "shell" {
		t.Errorf("the source's label = %q: a detach relabels the tile it was opened from only", got)
	}

	sa, err := attach(t, p, a)
	if err != nil {
		t.Fatalf("open the source: %v", err)
	}
	if s := fake.LastSession(); s.Key != a || s.OpenMode != tmux.ModeAttach {
		t.Errorf("the source opened session %q mode %v, want %q attached", s.Key, s.OpenMode, a)
	}
	_ = sa.detach(t)
}

// Two tiles on one session hold one attachment: the second evicts the first
// and reuses its PTY.
func TestASecondTileOnOneSessionTakesItOver(t *testing.T) {
	fake := shellsvctest.New()
	p, a := shellPluginWithTile(t, fake)
	b := cloneShell(t, p, a, 2)

	first, err := attach(t, p, a)
	if err != nil {
		t.Fatal(err)
	}
	second, err := attach(t, p, b)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-first.done; err != nil {
		t.Errorf("the evicted holder ended with %v, want a clean end", err)
	}
	first.cancel()
	if n := fake.SessionCount(); n != 1 {
		t.Errorf("opened %d PTYs, want 1: one attachment per session", n)
	}
	_ = second.detach(t)
}

// A session someone started and that is gone stays gone behind every face
// naming it; one never started is created by whichever tile opens it first.
func TestAStartedSessionIsNeverFabricatedBehindAFace(t *testing.T) {
	fake := shellsvctest.New()
	p, a := shellPluginWithTile(t, fake)
	b := cloneShell(t, p, a, 2)
	if _, err := p.SetTile(context.Background(), &gridwellv1.SetTileRequest{TileId: a,
		Tile: &gridwellv1.Tile{Kind: "shell"}, Preview: []byte("face")}); err != nil {
		t.Fatal(err)
	}
	_, err := attach(t, p, b)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("opening a faceless clone of a started, gone session = %v, want FailedPrecondition", err)
	}
	if n := fake.SessionCount(); n != 0 {
		t.Errorf("opened %d sessions behind a face", n)
	}
}

// A row that is not there refuses with NotFound and spawns nothing, and its
// liveness probe answers dead.
func TestAMissingShellRowRefusesAndSpawnsNothing(t *testing.T) {
	fake := shellsvctest.New()
	p, _ := shellPluginWithTile(t, fake)
	_, err := attach(t, p, "999")
	if status.Code(err) != codes.NotFound {
		t.Fatalf("OpenShell on a missing row = %v, want NotFound", err)
	}
	if n := fake.SessionCount(); n != 0 {
		t.Errorf("a missing row spawned %d sessions", n)
	}
	resp, err := p.ShellSessionAlive(context.Background(), &gridwellv1.ShellSessionAliveRequest{TileId: "999"})
	if err != nil || resp.Alive {
		t.Errorf("probe of a missing row = (%v, %v), want dead", resp, err)
	}
}

// A clone's probe asks its session, not its own id.
func TestACloneProbesTheSessionItNames(t *testing.T) {
	fake := shellsvctest.New()
	p, a := shellPluginWithTile(t, fake)
	b := cloneShell(t, p, a, 2)
	fake.SetAlive(a, true)
	resp, err := p.ShellSessionAlive(context.Background(), &gridwellv1.ShellSessionAliveRequest{TileId: b})
	if err != nil || !resp.Alive {
		t.Errorf("the clone's probe = (%v, %v), want its source's live session", resp, err)
	}
}

// The trash keeps a session, destroying one of two rows keeps it, destroying
// the last kills it; the boot sweep spares a session a copy still names after
// its starter is gone.
func TestASessionDiesWithTheLastRowNamingIt(t *testing.T) {
	ctx := context.Background()
	fake := shellsvctest.New()
	p, a := shellPluginWithTile(t, fake)
	b := cloneShell(t, p, a, 2)
	fake.SetAlive(a, true)
	del := func(id string) {
		t.Helper()
		if _, err := p.DeleteTile(ctx, &gridwellv1.DeleteTileRequest{TileId: id}); err != nil {
			t.Fatal(err)
		}
	}
	del(a) // to the trash
	del(a) // destroyed, b still names the session
	if slices.Contains(fake.Killed(), a) {
		t.Fatal("destroying the source killed the session its clone names")
	}
	fake.SetAlive("4242", true) // a session no row names
	if n, err := p.CleanupOrphanedShells(ctx); err != nil || n != 1 {
		t.Fatalf("orphan sweep = (%d, %v), want the one unnamed session", n, err)
	}
	if slices.Contains(fake.Killed(), a) {
		t.Fatal("the orphan sweep killed a session a clone names")
	}
	del(b)
	if slices.Contains(fake.Killed(), a) {
		t.Fatal("a trashed clone still names the session; the trash must keep it")
	}
	del(b)
	if !slices.Contains(fake.Killed(), a) {
		t.Errorf("killed %v, want the session %s once its last row was destroyed", fake.Killed(), a)
	}
}
