package shellsvc_test

import (
	"context"
	"errors"
	"testing"

	"github.com/josephburnett/gridwell/plugins/local/shellsvc"
	"github.com/josephburnett/gridwell/plugins/local/shellsvc/shellsvctest"
	"github.com/josephburnett/gridwell/plugins/local/tmux"
)

func TestAcquire_FreshTileCreates(t *testing.T) {
	fake := shellsvctest.New() // tile not alive
	m := shellsvc.NewManager(fake)

	sess, _, err := m.Acquire("1", true /*allowCreate*/, 100, 40)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if sess == nil {
		t.Fatal("nil session")
	}
	last := fake.LastSession()
	if last.OpenMode != tmux.ModeCreate {
		t.Errorf("mode = %v, want ModeCreate", last.OpenMode)
	}
	if last.InitialCols != 100 || last.InitialRows != 40 {
		t.Errorf("size = %dx%d, want 100x40", last.InitialCols, last.InitialRows)
	}
}

func TestAcquire_LiveSessionAttaches(t *testing.T) {
	fake := shellsvctest.New()
	fake.SetAlive("1", true) // a tmux session already exists
	m := shellsvc.NewManager(fake)

	if _, _, err := m.Acquire("1", false /*allowCreate*/, 80, 24); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if got := fake.LastSession().OpenMode; got != tmux.ModeAttach {
		t.Errorf("mode = %v, want ModeAttach", got)
	}
}

func TestAcquire_SnapshotNoSessionRejected(t *testing.T) {
	fake := shellsvctest.New() // not alive
	m := shellsvc.NewManager(fake)

	// allowCreate=false (a snapshotted tile) + no live session → ErrSessionGone:
	// we must not fabricate a fresh bash behind the JPEG.
	if _, _, err := m.Acquire("1", false, 80, 24); !errors.Is(err, shellsvc.ErrSessionGone) {
		t.Fatalf("err = %v, want ErrSessionGone", err)
	}
	if fake.SessionCount() != 0 {
		t.Errorf("opened %d sessions, want 0", fake.SessionCount())
	}
}

func TestAcquire_TakeoverReusesPTYAndEvictsOld(t *testing.T) {
	fake := shellsvctest.New()
	m := shellsvc.NewManager(fake)

	sess1, stopOld1, err := m.Acquire("1", true, 80, 24)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	// A second acquire (refresh from another pane) reuses the SAME PTY and
	// signals the first holder to stop.
	sess2, _, err := m.Acquire("1", true, 80, 24)
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	if sess1 != sess2 {
		t.Error("takeover opened a new PTY; want the same session reused")
	}
	if fake.SessionCount() != 1 {
		t.Errorf("opened %d sessions, want 1 (reuse)", fake.SessionCount())
	}
	select {
	case <-stopOld1:
		// good: the first holder was evicted.
	default:
		t.Error("first holder's stopOld was not closed on takeover")
	}
}

func TestReleaseClosesSessionAndFiresOnDetach(t *testing.T) {
	fake := shellsvctest.New()
	m := shellsvc.NewManager(fake)

	sess, stopOld, _ := m.Acquire("1", true, 80, 24)
	detached := false
	m.Release("1", sess, stopOld, func() { detached = true })

	if !sess.(*shellsvctest.FakeSession).IsClosed() {
		t.Error("session not closed on release")
	}
	if !detached {
		t.Error("onDetach not fired")
	}
}

func TestCleanupOrphans_KillsGoneTiles(t *testing.T) {
	fake := shellsvctest.New()
	fake.SetAlive("1", true) // tile still exists
	fake.SetAlive("2", true) // tile was deleted (orphan)
	m := shellsvc.NewManager(fake)

	exists := func(tileID string) (bool, error) { return tileID == "1", nil }
	killed, err := m.CleanupOrphans(context.Background(), exists)
	if err != nil {
		t.Fatalf("CleanupOrphans: %v", err)
	}
	if killed != 1 {
		t.Errorf("killed %d, want 1", killed)
	}
	if got := fake.Killed(); len(got) != 1 || got[0] != "2" {
		t.Errorf("killed ids = %v, want [2]", got)
	}
}
