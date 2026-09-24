package tracewire

import (
	"encoding/json"
	"testing"
)

// The record shape is the contract two repositories' halves write against, so
// the field names and their order are pinned to a literal rather than to the
// struct.
func TestClientRecordIsTheContractLine(t *testing.T) {
	b, err := json.Marshal(Record{
		Origin: OriginClient,
		Src:    "nav",
		Kind:   "push",
		Msg:    "descend a1b2c3d",
		KV:     map[string]string{"req": "k3f9x2a", "grid": "a1b2c3d"},
		CID:    "cid7abc",
		CT:     1727000000123,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const want = `{"origin":"client","src":"nav","kind":"push","msg":"descend a1b2c3d",` +
		`"kv":{"grid":"a1b2c3d","req":"k3f9x2a"},"cid":"cid7abc","ct":1727000000123}`
	if string(b) != want {
		t.Errorf("record marshals to\n  %s\nwant\n  %s", b, want)
	}
}

// Seq and T are the node's stamps. An emitter that leaves them zero must send
// a line the node can stamp, rather than one already claiming sequence 0.
func TestTheNodesStampsAreAbsentUntilStamped(t *testing.T) {
	b, err := json.Marshal(Record{Origin: OriginClient, Src: "trace", Kind: "drop", Msg: "x"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"seq", "t"} {
		if _, ok := got[k]; ok {
			t.Errorf("an unstamped record carries %q: %s", k, b)
		}
	}
	stamped, err := json.Marshal(Record{Seq: 4, T: 1727000000999, Origin: OriginElectron, Src: "main", Kind: "view", Msg: "x"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const wantStamped = `{"seq":4,"t":1727000000999,"origin":"electron","src":"main","kind":"view","msg":"x"}`
	if string(stamped) != wantStamped {
		t.Errorf("stamped record marshals to\n  %s\nwant\n  %s", stamped, wantStamped)
	}
}
