package urlstream

import (
	"encoding/json"
	"testing"

	"github.com/josephburnett/gridwell/internal/rpc"
	"github.com/josephburnett/gridwell/internal/urldriver"
)

func TestViewportPayloadRoundTrip(t *testing.T) {
	got := ViewportPayload(1024, 768)
	var msg rpc.URLStreamMessage
	if err := json.Unmarshal([]byte(got), &msg); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if msg.Kind != "viewport" {
		t.Errorf("Kind = %q, want viewport", msg.Kind)
	}
	if msg.Width != 1024 || msg.Height != 768 {
		t.Errorf("size = (%d, %d), want (1024, 768)", msg.Width, msg.Height)
	}
}

func TestInputPayloadOmitsUnusedFields(t *testing.T) {
	ev := urldriver.InputEvent{
		Kind:   urldriver.InputMouseMove,
		X:      100,
		Y:      200,
		Button: "left",
	}
	got, err := InputPayload(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// DeltaY / Key / Code / Modifiers are zero and have omitempty —
	// they should not appear.
	for _, k := range []string{"delta_y", "key", "code", "modifiers"} {
		if containsKey(got, k) {
			t.Errorf("InputPayload should omit %q for zero-valued field, got: %s", k, got)
		}
	}
	// The kind / X / Y / button SHOULD appear.
	for _, k := range []string{"kind", "x", "y", "button"} {
		if !containsKey(got, k) {
			t.Errorf("InputPayload should include %q, got: %s", k, got)
		}
	}
}

func TestInputPayloadFullKey(t *testing.T) {
	ev := urldriver.InputEvent{
		Kind:      urldriver.InputKeyDown,
		Key:       "a",
		Code:      "KeyA",
		Modifiers: 3,
	}
	got, err := InputPayload(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var msg rpc.URLStreamMessage
	if err := json.Unmarshal([]byte(got), &msg); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if msg.Kind != string(urldriver.InputKeyDown) || msg.Key != "a" ||
		msg.Code != "KeyA" || msg.Modifiers != 3 {
		t.Errorf("round-trip mismatch: %+v", msg)
	}
}

func TestParseNavMessage(t *testing.T) {
	payload, _ := json.Marshal(rpc.URLStreamServerMessage{Kind: "nav", URL: "https://example.com/x"})
	url, ok := ParseNavMessage(string(payload))
	if !ok {
		t.Fatal("want ok=true for nav message")
	}
	if url != "https://example.com/x" {
		t.Errorf("url = %q, want https://example.com/x", url)
	}
}

func TestParseNavMessageRejectsOtherKinds(t *testing.T) {
	payload, _ := json.Marshal(rpc.URLStreamServerMessage{Kind: "other", URL: "x"})
	if _, ok := ParseNavMessage(string(payload)); ok {
		t.Error("non-nav kinds should return ok=false")
	}
}

func TestParseNavMessageRejectsInvalidJSON(t *testing.T) {
	if _, ok := ParseNavMessage("{not json"); ok {
		t.Error("invalid JSON should return ok=false")
	}
}

// containsKey is a sloppy "does this JSON string mention the key" check
// — fine for the omitempty tests, where we just need to know whether
// the field appeared at all in the marshalled output.
func containsKey(s, key string) bool {
	q := `"` + key + `"`
	for i := 0; i+len(q) <= len(s); i++ {
		if s[i:i+len(q)] == q {
			return true
		}
	}
	return false
}
