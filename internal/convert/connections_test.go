package convert_test

// The connections converter's roundtrip gate: rows created through the
// OLD picker flow emit yaml that reconciles back onto the same DB as a
// perfect no-op (names verbatim, params canonical-equal, labels kept,
// tombstones traveling as retired_names).

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/josephburnett/gridwell/api/pluginmeta"
	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/convert"
	"github.com/josephburnett/gridwell/internal/remote"
)

// connSpecs maps yaml declarations to the transport's vocabulary the way
// the serve wiring does (test-local: production maps via connections_json
// in resolvePluginBinaries/injectConnections).
func connSpecs(conns []config.ConnectionConfig) []remote.ConnSpec {
	out := make([]remote.ConnSpec, len(conns))
	for i, c := range conns {
		out[i] = remote.ConnSpec{Name: c.Name, Label: c.Label, Host: c.Host, User: c.User,
			Port: c.Port, Addr: c.Addr, Key: c.Key, KnownHosts: c.KnownHosts}
	}
	return out
}

func TestConnectionsRoundtrip(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "remote.db")
	if err := pluginmeta.Create(dbPath, "remuuid", "remote"); err != nil {
		t.Fatal(err)
	}
	db, err := remote.OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Live in the old world: a direct connection (renamed by the user), an
	// ssh connection (auto label), and a deleted one (tombstoned).
	mk := func(ns, params, label string) {
		t.Helper()
		c, err := db.CreateWithNS(ctx, ns, "")
		if err != nil {
			t.Fatal(err)
		}
		if c, err = db.SetParams(ctx, c.ID, c.Version, params); err != nil {
			t.Fatal(err)
		}
		if label != "" {
			if _, err = db.Rename(ctx, c.ID, c.Version, label); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("geneva1", `{"addr":"localhost:10011"}`, "Geneva")
	mk("rtbhome", `{"host":"192.168.88.5","user":"joe","key":"~/.ssh/rtb.local"}`, "")
	dead, err := db.CreateWithNS(ctx, "olddead", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Tombstone(ctx, dead.ID, dead.Version); err != nil {
		t.Fatal(err)
	}

	conns, retired, err := convert.Connections(dbPath, "remuuid")
	if err != nil {
		t.Fatal(err)
	}
	if len(conns) != 2 || conns[0].Name != "geneva1" || conns[0].Label != "Geneva" ||
		conns[0].Addr != "localhost:10011" || conns[1].Name != "rtbhome" || conns[1].Label != "" {
		t.Fatalf("emitted: %+v", conns)
	}
	if len(retired) != 1 || retired[0] != "olddead" {
		t.Fatalf("retired: %v", retired)
	}

	// The roundtrip: syncing the emitted yaml onto the SAME DB is a
	// no-op (no version churn, nothing tombstoned, nothing created).
	before, _ := db.GetByNS(ctx, "geneva1")
	if _, err := remote.SyncConfig(ctx, db, connSpecs(conns), retired); err != nil {
		t.Fatalf("roundtrip sync: %v", err)
	}
	after, _ := db.GetByNS(ctx, "geneva1")
	if after.Version != before.Version || after.Deleted {
		t.Fatalf("roundtrip churned: %d → %d deleted=%v", before.Version, after.Version, after.Deleted)
	}
	ssh, _ := db.GetByNS(ctx, "rtbhome")
	if ssh.Deleted || ssh.AltText != "joe@192.168.88.5" {
		t.Fatalf("ssh row churned: %+v", ssh)
	}
}

func TestConnectionsRefusesPendingStub(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "remote.db")
	if err := pluginmeta.Create(dbPath, "remuuid", "remote"); err != nil {
		t.Fatal(err)
	}
	db, err := remote.OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.CreateWithNS(ctx, "pending1", "half-made"); err != nil {
		t.Fatal(err)
	}
	_, _, err = convert.Connections(dbPath, "remuuid")
	if err == nil || !strings.Contains(err.Error(), "no committed params") {
		t.Fatalf("pending stub not refused: %v", err)
	}
}

func TestConvertNeverServedPluginIsEmpty(t *testing.T) {
	// init stamps identity; the schema appears on first open. Converting
	// a never-served fs DB yields an empty memory DB, not an error.
	dbPath := filepath.Join(t.TempDir(), "fs.db")
	if err := pluginmeta.Create(dbPath, "freshfs", "fs"); err != nil {
		t.Fatal(err)
	}
	res, err := convert.FS(dbPath, filepath.Join(t.TempDir(), "mem.db"), "freshfs", "fs", "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if res.Grids != 0 || res.Tiles != 0 {
		t.Fatalf("expected empty conversion: %+v", res)
	}
}
