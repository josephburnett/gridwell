package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunInitCreatesDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	rc := RunInit([]string{"--db", dbPath})
	if rc != 0 {
		t.Fatalf("init returned %d", rc)
	}
	st, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Size() == 0 {
		t.Error("db file is empty")
	}
}

func TestRunInitIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	if rc := RunInit([]string{"--db", dbPath}); rc != 0 {
		t.Fatal("first init failed")
	}
	if rc := RunInit([]string{"--db", dbPath}); rc != 0 {
		t.Errorf("second init returned %d", rc)
	}
}
