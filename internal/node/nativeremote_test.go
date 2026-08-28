package node

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	gridwellv1 "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/pluginmeta"
	"github.com/josephburnett/gridwell/internal/remote"
)

// The yaml→transport seam end to end: serve marshals []remote.ConnSpec
// into the flat config as connections_json, and the native factory must
// unmarshal it, sync the store in config mode, and list the connection —
// the whole hop internal/node had zero tests across.
func TestNativeRemoteFactoryMaterializesConnections(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "remote.db")
	if err := pluginmeta.Create(dbPath, "rmt1234", "remote"); err != nil {
		t.Fatal(err)
	}
	// Addr points at a closed local port so the boot-time ConnectAll
	// fails fast instead of dialing out.
	blob, err := json.Marshal([]remote.ConnSpec{{
		Name: "rtb", Label: "RTB", Addr: "127.0.0.1:1", Key: "/k", KnownHosts: "/kh",
	}})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NativeRemoteFactory(map[string]string{
		"db_file":          dbPath,
		"uuid":             "rmt1234",
		"kind":             "remote",
		"connections_json": string(blob),
		"retired_json":     `["oldname"]`,
	})
	if err != nil {
		t.Fatal(err)
	}
	g, err := srv.GetGrid(context.Background(), &gridwellv1.GetGridRequest{GridId: "0"})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, tile := range g.Tiles {
		if tile.AltText == "RTB" {
			found = true
		}
	}
	if !found {
		t.Fatalf("declared connection did not materialize on the connection grid: %+v", g.Tiles)
	}
}

// The retired-name tombstone crosses the same seam: a declared name that
// is also retired must refuse the boot loudly, not half-apply.
func TestNativeRemoteFactoryRefusesRetiredDeclaredName(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "remote.db")
	if err := pluginmeta.Create(dbPath, "rmt1234", "remote"); err != nil {
		t.Fatal(err)
	}
	blob, _ := json.Marshal([]remote.ConnSpec{{Name: "rtb", Addr: "127.0.0.1:1"}})
	_, err := NativeRemoteFactory(map[string]string{
		"db_file":          dbPath,
		"uuid":             "rmt1234",
		"kind":             "remote",
		"connections_json": string(blob),
		"retired_json":     `["rtb"]`,
	})
	if err == nil {
		t.Fatal("a retired name re-declared must refuse — a tombstoned namespace segment never returns")
	}
}
