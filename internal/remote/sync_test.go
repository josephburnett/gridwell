package remote

// SyncConfig: server.yaml owns the connection set (v2 #269, reversing
// #199). These pin the reconcile semantics — names verbatim, idempotence
// (no version churn on a no-op boot), tombstone on removal, retirement
// forever — and the config-mode refusals on every mutating verb.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func openSyncDB(t *testing.T) *DB {
	t.Helper()
	db, err := OpenDB(filepath.Join(t.TempDir(), "remote.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func specs() []ConnSpec {
	return []ConnSpec{
		{Name: "geneva1", Label: "geneva", Addr: "127.0.0.1:10011"},
		{Name: "rtbhome", Host: "192.168.88.5", User: "joe", Key: "~/.ssh/rtb.local"},
	}
}

func TestSyncMaterializesNamesVerbatim(t *testing.T) {
	db := openSyncDB(t)
	ctx := context.Background()
	live, err := SyncConfig(ctx, db, specs(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 2 || live[0] != "geneva1" || live[1] != "rtbhome" {
		t.Fatalf("live = %v", live)
	}
	g, err := db.GetByNS(ctx, "geneva1")
	if err != nil || g.Deleted {
		t.Fatalf("geneva1 row: %+v err=%v", g, err)
	}
	if g.AltText != "geneva" {
		t.Fatalf("label = %q, want geneva", g.AltText)
	}
	p, err := ParseParams([]byte(g.Params))
	if err != nil || p.Addr != "127.0.0.1:10011" || p.Host != "" {
		t.Fatalf("params: %+v err=%v", p, err)
	}
	r, _ := db.GetByNS(ctx, "rtbhome")
	if r.AltText != "joe@192.168.88.5" {
		t.Fatalf("auto label = %q", r.AltText)
	}
}

func TestSyncIsIdempotent(t *testing.T) {
	db := openSyncDB(t)
	ctx := context.Background()
	if _, err := SyncConfig(ctx, db, specs(), nil); err != nil {
		t.Fatal(err)
	}
	before, _ := db.GetByNS(ctx, "geneva1")
	if _, err := SyncConfig(ctx, db, specs(), nil); err != nil {
		t.Fatal(err)
	}
	after, _ := db.GetByNS(ctx, "geneva1")
	if after.Version != before.Version {
		t.Fatalf("a no-op boot churned the version: %d → %d", before.Version, after.Version)
	}
}

func TestSyncParamsChangeUpdates(t *testing.T) {
	db := openSyncDB(t)
	ctx := context.Background()
	if _, err := SyncConfig(ctx, db, specs(), nil); err != nil {
		t.Fatal(err)
	}
	changed := specs()
	changed[0].Addr = "127.0.0.1:20022"
	if _, err := SyncConfig(ctx, db, changed, nil); err != nil {
		t.Fatal(err)
	}
	g, _ := db.GetByNS(ctx, "geneva1")
	p, _ := ParseParams([]byte(g.Params))
	if p.Addr != "127.0.0.1:20022" {
		t.Fatalf("params not updated: %+v", p)
	}
	if g.RemoteRoot != "" {
		t.Fatal("params change must clear the remote-root cache (may name another machine)")
	}
}

func TestSyncRemovalRetiresForever(t *testing.T) {
	db := openSyncDB(t)
	ctx := context.Background()
	if _, err := SyncConfig(ctx, db, specs(), nil); err != nil {
		t.Fatal(err)
	}
	// geneva1 leaves the config: tombstoned.
	if _, err := SyncConfig(ctx, db, specs()[1:], nil); err != nil {
		t.Fatal(err)
	}
	g, err := db.GetByNS(ctx, "geneva1")
	if err != nil || !g.Deleted {
		t.Fatalf("removed connection not tombstoned: %+v err=%v", g, err)
	}
	// The name never returns.
	if _, err := SyncConfig(ctx, db, specs(), nil); err == nil || !strings.Contains(err.Error(), "RETIRED") {
		t.Fatalf("reusing a retired name not refused: %v", err)
	}
}

func TestSyncRetiredNamesReserveOnFreshDB(t *testing.T) {
	db := openSyncDB(t)
	ctx := context.Background()
	if _, err := SyncConfig(ctx, db, specs()[1:], []string{"geneva1"}); err != nil {
		t.Fatal(err)
	}
	g, err := db.GetByNS(ctx, "geneva1")
	if err != nil || !g.Deleted {
		t.Fatalf("retired name not reserved: %+v err=%v", g, err)
	}
	// Declaring a retired name is refused up front.
	if _, err := SyncConfig(ctx, db, specs(), []string{"geneva1"}); err == nil || !strings.Contains(err.Error(), "RETIRED") {
		t.Fatalf("declared retired name not refused: %v", err)
	}
}

func TestSyncRefusesBadNames(t *testing.T) {
	db := openSyncDB(t)
	ctx := context.Background()
	if _, err := SyncConfig(ctx, db, []ConnSpec{{Name: "has/slash", Addr: "x:1"}}, nil); err == nil {
		t.Fatal("slash name accepted")
	}
	if _, err := SyncConfig(ctx, db, []ConnSpec{{Name: "a1", Addr: "x:1"}, {Name: "a1", Addr: "y:1"}}, nil); err == nil {
		t.Fatal("duplicate name accepted")
	}
	if _, err := SyncConfig(ctx, db, []ConnSpec{{Name: "noaddr"}}, nil); err == nil {
		t.Fatal("empty params accepted (need addr or host)")
	}
}

func TestSyncConfigLabelLatches(t *testing.T) {
	// A config label is the USER speaking (yaml is theirs): it must latch
	// alt_user, or connTile's auto-label override displays joe@host
	// instead of the declared label (the work-laptop cutover bug,
	// 2026-08-23).
	db := openSyncDB(t)
	ctx := context.Background()
	if _, err := SyncConfig(ctx, db, specs(), nil); err != nil {
		t.Fatal(err)
	}
	g, err := db.GetByNS(ctx, "geneva1")
	if err != nil {
		t.Fatal(err)
	}
	if !g.AltUser {
		t.Fatal("config label did not latch alt_user — the display falls back to the auto-label")
	}
	if g.AltText != "geneva" {
		t.Fatalf("alt = %q, want the config label", g.AltText)
	}
	// No label declared: the auto-label rules, unlatched.
	r, _ := db.GetByNS(ctx, "rtbhome")
	if r.AltUser {
		t.Fatal("an auto-labeled connection must stay unlatched")
	}
}

func TestConfigModeDropsCreationSchema(t *testing.T) {
	// The declaration channel is the gate: no create schema in config
	// mode, so the picker renders pick-only (no form, no rename/delete).
	db := openSyncDB(t)
	s := New(db, nil, "")
	info, err := s.Info(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.CreateSchemas) == 0 {
		t.Fatal("legacy mode must declare the creation schema")
	}
	s.SetConfigMode(true)
	info, err = s.Info(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.CreateSchemas) != 0 {
		t.Fatal("config mode must not declare a creation schema — the picker would render a form that can only fail")
	}
}
