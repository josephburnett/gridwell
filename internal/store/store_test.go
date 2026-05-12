package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// newTestStore opens an in-memory store and seeds deterministic clocks/IDs so
// tests are reproducible. Each test gets a fresh store.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.UseFastHashing()

	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return fixed })
	var counter int
	s.SetIDGenerator(func() string {
		counter++
		return "obj-" + strings.Repeat("0", 28-len("obj-")) + itoa(counter)
	})
	return s
}

// itoa avoids importing strconv just for tests; it returns a 4-digit hex of n.
func itoa(n int) string {
	const hex = "0123456789abcdef"
	out := []byte{'0', '0', '0', '0'}
	for i := 3; i >= 0 && n > 0; i-- {
		out[i] = hex[n&0xf]
		n >>= 4
	}
	return string(out)
}

func TestOpenAppliesSchema(t *testing.T) {
	s := newTestStore(t)
	rows, err := s.db.Query(
		`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		got = append(got, n)
	}
	want := map[string]bool{
		"groups": true, "users": true, "user_groups": true,
		"grids": true, "tiles": true, "blobs": true, "sessions": true,
	}
	for _, g := range got {
		delete(want, g)
	}
	if len(want) > 0 {
		t.Errorf("missing tables: %v (have %v)", want, got)
	}
}

func TestCreateUserHappyPath(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u, err := s.CreateUser(ctx, "alice", "secret")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if u.Username != "alice" {
		t.Errorf("username = %q", u.Username)
	}
	if u.RootGridID == 0 {
		t.Error("root_grid_id zero")
	}
	if u.PrimaryGroupID == 0 {
		t.Error("primary_group_id zero")
	}

	// Re-fetch.
	got, err := s.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Username != "alice" {
		t.Errorf("get back username = %q", got.Username)
	}

	// Membership in primary group.
	in, err := s.IsInGroup(ctx, u.ID, u.PrimaryGroupID)
	if err != nil || !in {
		t.Errorf("IsInGroup = %v, %v", in, err)
	}

	// Authentication.
	if _, err := s.AuthenticateUser(ctx, "alice", "secret"); err != nil {
		t.Errorf("authenticate good: %v", err)
	}
	if _, err := s.AuthenticateUser(ctx, "alice", "bad"); err == nil {
		t.Error("authenticate bad: expected error")
	}
	if _, err := s.AuthenticateUser(ctx, "nobody", "x"); err == nil {
		t.Error("authenticate unknown: expected error")
	}
}

func TestCreateUserRejectsDuplicate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, "alice", "p"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "alice", "q"); err == nil {
		t.Error("expected duplicate error")
	}
}

func TestCreateUserValidates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, "", "p"); err == nil {
		t.Error("expected error for empty username")
	}
	if _, err := s.CreateUser(ctx, "alice", ""); err == nil {
		t.Error("expected error for empty password")
	}
	if _, err := s.CreateUser(ctx, "  ", "p"); err == nil {
		t.Error("expected error for whitespace username")
	}
}

func TestSessionCreateAndLookup(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u, err := s.CreateUser(ctx, "alice", "p")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := s.CreateSession(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 64 {
		t.Errorf("token length %d", len(tok))
	}
	uid, err := s.LookupSession(ctx, tok)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if uid != u.ID {
		t.Errorf("uid = %d, want %d", uid, u.ID)
	}

	// Unknown token.
	if _, err := s.LookupSession(ctx, "nope"); err == nil {
		t.Error("expected ErrNotFound for unknown token")
	}

	// Logout.
	if err := s.DeleteSession(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupSession(ctx, tok); err == nil {
		t.Error("expected error after logout")
	}
	// Idempotent delete.
	if err := s.DeleteSession(ctx, tok); err != nil {
		t.Errorf("idempotent delete: %v", err)
	}
}

func TestSessionExpires(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })

	u, err := s.CreateUser(ctx, "alice", "p")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := s.CreateSession(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Advance past expiry.
	advanced := now.Add(SessionTTL + time.Hour)
	s.SetClock(func() time.Time { return advanced })

	if _, err := s.LookupSession(ctx, tok); err != ErrSessionExpired {
		t.Errorf("expected ErrSessionExpired, got %v", err)
	}

	n, err := s.SweepExpiredSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("swept %d, want 1", n)
	}
}

func TestPermForGridAndNodeNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, _, err := s.permForGrid(ctx, s.db, 1, 9999); err != ErrNotFound {
		t.Errorf("permForGrid missing: got %v", err)
	}
	if _, _, err := s.permForTile(ctx, s.db, 1, 9999); err != ErrNotFound {
		t.Errorf("permForTile missing: got %v", err)
	}
}
