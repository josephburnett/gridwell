package convert_test

// Env-gated real-data check for the connections emitter (the fs twin's
// pattern): point at a COPY of a production transport DB.
//
//	GRIDWELL_REALGATE_REMOTE_DB=... GRIDWELL_REALGATE_REMOTE_UUID=... \
//	go test -run TestRealDataConnections ./internal/convert/

import (
	"os"
	"testing"

	"github.com/josephburnett/gridwell/internal/convert"
)

func TestRealDataConnections(t *testing.T) {
	dbPath := os.Getenv("GRIDWELL_REALGATE_REMOTE_DB")
	uuid := os.Getenv("GRIDWELL_REALGATE_REMOTE_UUID")
	if dbPath == "" || uuid == "" {
		t.Skip("set GRIDWELL_REALGATE_REMOTE_{DB,UUID} to run")
	}
	conns, retired, err := convert.Connections(dbPath, uuid)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("connections: %+v", conns)
	t.Logf("retired: %v", retired)
	if len(conns) == 0 && len(retired) == 0 {
		t.Fatal("nothing emitted")
	}
}
