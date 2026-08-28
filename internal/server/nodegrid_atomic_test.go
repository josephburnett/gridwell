package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// persist must be atomic (write-temp + rename): loadView discards an
// unparseable state file WHOLESALE, so a torn write loses every launcher
// placement and the landing viewport at once. A crash mid-write can't be
// triggered from a test, so the property is asserted through the
// equivalent observable: a concurrent reader must never see a partial
// file — under the old O_TRUNC WriteFile it reliably does.
func TestPersistNeverTearsUnderAReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node-view.json")
	n := &nodeGrid{statePath: path}
	n.view.Tiles = map[string]nodeTilePos{}
	for i := 0; i < 5000; i++ {
		n.view.Tiles[fmt.Sprintf("plugin-%05d", i)] = nodeTilePos{X: int64(i), Y: int64(i)}
	}
	if err := n.persist(n.view); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if err := n.persist(n.view); err != nil {
				t.Errorf("persist: %v", err)
				return
			}
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err != nil {
			close(stop)
			<-writerDone
			t.Fatalf("reader saw a missing/unreadable state file: %v", err)
		}
		var v nodeView
		if err := json.Unmarshal(data, &v); err != nil {
			close(stop)
			<-writerDone
			t.Fatalf("reader saw a TORN state file (%d bytes): %v — a crash here loses every placement", len(data), err)
		}
	}
	close(stop)
	<-writerDone
}
