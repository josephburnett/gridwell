package trace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSeqIsTheOneTotalOrderAndTheRingWrapsOnTheOldest(t *testing.T) {
	r := New(3)
	for i := 0; i < 5; i++ {
		r.Emit(Record{Src: "store", Kind: "write", Msg: []string{"a", "b", "c", "d", "e"}[i]})
	}
	got := r.Snapshot()
	if len(got) != 3 {
		t.Fatalf("held %d records, want the capacity 3", len(got))
	}
	var msgs []string
	for i, rec := range got {
		msgs = append(msgs, rec.Msg)
		if rec.Seq != int64(i+3) {
			t.Errorf("record %d has seq %d, want %d: seq counts every emit, not every slot", i, rec.Seq, i+3)
		}
		if rec.Origin != OriginNode {
			t.Errorf("record %d origin = %q, want the node's own", i, rec.Origin)
		}
		if _, err := time.Parse(time.RFC3339Nano, rec.T); err != nil {
			t.Errorf("record %d time %q: %v", i, rec.T, err)
		}
	}
	if strings.Join(msgs, "") != "cde" {
		t.Errorf("snapshot = %v, want the last three in seq order", msgs)
	}
}

func TestAnOverlongMessageIsTruncatedNotDropped(t *testing.T) {
	r := New(2)
	r.Emit(Record{Src: "log", Kind: "log", Msg: strings.Repeat("x", MaxMsgBytes+500)})
	got := r.Snapshot()
	if len(got) != 1 {
		t.Fatalf("held %d records, want the overlong one kept", len(got))
	}
	if len(got[0].Msg) != MaxMsgBytes {
		t.Errorf("msg is %d bytes, want it capped at %d", len(got[0].Msg), MaxMsgBytes)
	}
}

// A cut through a multi-byte rune must not leave the line unencodable.
func TestTruncationLeavesValidUTF8(t *testing.T) {
	r := New(2)
	r.Emit(Record{Msg: strings.Repeat("a", MaxMsgBytes-1) + "é" + "tail"})
	got := r.Snapshot()[0]
	if !isValidJSONRoundTrip(t, got) {
		t.Fatalf("truncated msg does not round-trip: %q", got.Msg)
	}
	if len(got.Msg) != MaxMsgBytes-1 {
		t.Errorf("msg is %d bytes, want the split rune dropped", len(got.Msg))
	}
}

func isValidJSONRoundTrip(t *testing.T, rec Record) bool {
	t.Helper()
	blob, err := json.Marshal(rec)
	if err != nil {
		return false
	}
	var back Record
	return json.Unmarshal(blob, &back) == nil && back.Msg == rec.Msg
}

func TestLogWriterMakesOneRecordPerLine(t *testing.T) {
	r := New(10)
	w := LogWriter(r)
	n, err := w.Write([]byte("first line\nsecond line\n"))
	if err != nil || n != len("first line\nsecond line\n") {
		t.Fatalf("Write = %d, %v", n, err)
	}
	got := r.Snapshot()
	if len(got) != 2 {
		t.Fatalf("held %d records, want one per line", len(got))
	}
	for i, want := range []string{"first line", "second line"} {
		if got[i].Msg != want || got[i].Src != "log" || got[i].Kind != "log" || got[i].Origin != OriginNode {
			t.Errorf("record %d = %+v, want the node's log line %q", i, got[i], want)
		}
	}
}

func TestIngestStampsTheNodesOrderAndForcesTheOrigin(t *testing.T) {
	r := New(10)
	body := strings.Join([]string{
		`{"origin":"client","src":"nav","kind":"nav","msg":"descend","kv":{"req":"k3f9x2a"},"cid":"c1","ct":42}`,
		`{"origin":"electron","src":"webviews","kind":"frame","msg":"sync"}`,
		`{"origin":"node","src":"liar","kind":"log","msg":"not from the node"}`,
		`{"origin":"","src":"unnamed","kind":"log","msg":"no origin"}`,
	}, "\n")
	n, err := r.Ingest(strings.NewReader(body))
	if err != nil || n != 4 {
		t.Fatalf("Ingest = %d, %v", n, err)
	}
	got := r.Snapshot()
	wantOrigins := []string{OriginClient, OriginElectron, OriginClient, OriginClient}
	for i, rec := range got {
		if rec.Origin != wantOrigins[i] {
			t.Errorf("record %d origin = %q, want %q: a record that came through the door is never the node's", i, rec.Origin, wantOrigins[i])
		}
		if rec.Seq != int64(i+1) || rec.T == "" {
			t.Errorf("record %d = seq %d t %q, want the node's stamp", i, rec.Seq, rec.T)
		}
	}
	if got[0].Cid != "c1" || got[0].Ct != 42 || got[0].KV["req"] != "k3f9x2a" {
		t.Errorf("the sender's cid, ct and kv were not kept: %+v", got[0])
	}
}

func TestABadLineKeepsTheGoodLinesBeforeItAndNamesItsNumber(t *testing.T) {
	r := New(10)
	body := "{\"src\":\"a\",\"msg\":\"one\"}\n{\"src\":\"b\",\"msg\":\"two\"}\nnot json\n{\"src\":\"c\",\"msg\":\"three\"}\n"
	n, err := r.Ingest(strings.NewReader(body))
	if err == nil {
		t.Fatal("a malformed line was accepted")
	}
	if n != 2 {
		t.Errorf("Ingest kept %d records, want the 2 good lines before the bad one", n)
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("error %q does not name the line number", err)
	}
	if len(r.Snapshot()) != 2 {
		t.Errorf("ring holds %d records, want the good lines ingested", len(r.Snapshot()))
	}
}

func TestDumpWritesEveryRecordInSeqOrder(t *testing.T) {
	r := New(4)
	for _, m := range []string{"one", "two", "three"} {
		r.Emit(Record{Src: "store", Kind: "write", Msg: m})
	}
	dir := filepath.Join(t.TempDir(), "dumps")
	at := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	path, n, err := r.Dump(dir, at)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("Dump reported %d records, want 3", n)
	}
	if want := filepath.Join(dir, "trace-20260923-100000.jsonl"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	if !filepath.IsAbs(path) {
		t.Errorf("path %q is not absolute", path)
	}
	di, err := os.Stat(dir)
	if err != nil || di.Mode().Perm() != 0o700 {
		t.Fatalf("dumps dir mode = %v (%v), want 0700", di, err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("dump file mode = %v (%v), want 0600", fi, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("dump has %d lines, want 3", len(lines))
	}
	for i, line := range lines {
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		if rec.Seq != int64(i+1) {
			t.Errorf("line %d has seq %d, want %d", i+1, rec.Seq, i+1)
		}
	}
	// A dump is a reading: the ring still holds what it held.
	if len(r.Snapshot()) != 3 {
		t.Errorf("the ring lost records to a dump: %d left", len(r.Snapshot()))
	}
}
