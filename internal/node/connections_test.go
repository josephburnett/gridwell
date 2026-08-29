package node

// The yaml→transport connection seam: transportConfig is the ONE place
// server.yaml's vocabulary meets remote.ConnSpec. It used to build a
// hand-keyed map[string]any whose keys only matched ConnSpec by Go's
// default field-name marshaling — a field added to either side silently
// dropped on the wire. The typed literal plus this exhaustiveness pin
// make that drift a loud test failure instead.

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/remote"
)

func TestInjectConnectionsCarriesEveryField(t *testing.T) {
	cfg := &config.ServerConfig{
		ID: "n0de1",
		Connections: []config.ConnectionConfig{{
			Name: "con1", Label: "rtb", Host: "192.168.88.5", User: "joe",
			Port: 2222, Addr: "/r/federation.sock", Key: "~/.ssh/rtb",
			KnownHosts: "~/.ssh/known_hosts",
		}},
		RetiredNames: []string{"olddead"},
	}
	m, err := transportConfig("/h", cfg)
	if err != nil {
		t.Fatal(err)
	}
	if m["uuid"] != "n0de1" || m["kind"] != "remote" || m["db_file"] != "/h/db/n0de1/remote.db" {
		t.Fatalf("transport identity = %v", m)
	}
	var specs []remote.ConnSpec
	if err := json.Unmarshal([]byte(m["connections_json"]), &specs); err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 {
		t.Fatalf("specs = %+v", specs)
	}
	// Every ConnSpec field must survive the hop: the fixture sets every
	// config field non-zero, so a zero field here means the mapping (or
	// the parallel struct) lost it.
	v := reflect.ValueOf(specs[0])
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).IsZero() {
			t.Errorf("ConnSpec.%s was dropped crossing transportConfig — map it in node.go (and in config.ConnectionConfig if new)", v.Type().Field(i).Name)
		}
	}
	var retired []string
	if err := json.Unmarshal([]byte(m["retired_json"]), &retired); err != nil || len(retired) != 1 || retired[0] != "olddead" {
		t.Fatalf("retired_json = %v (%v)", retired, err)
	}
}
