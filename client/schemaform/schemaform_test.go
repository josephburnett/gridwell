package schemaform

import (
	"strings"
	"testing"
)

const sshSchema = `{
  "type": "object",
  "properties": {
    "host": {"type": "string", "title": "Host"},
    "user": {"type": "string", "title": "User"},
    "port": {"type": "number", "title": "Port"},
    "key":  {"type": "string", "title": "Key path", "format": "secret"},
    "mode": {"type": "string", "enum": ["fast", "safe"]}
  },
  "required": ["host", "user"]
}`

func TestParseFieldsSortedAndTyped(t *testing.T) {
	f, err := Parse(sshSchema)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, fd := range f.Fields {
		names = append(names, fd.Name)
	}
	if strings.Join(names, ",") != "host,key,mode,port,user" {
		t.Errorf("field order = %v (must be deterministic)", names)
	}
	byName := map[string]Field{}
	for _, fd := range f.Fields {
		byName[fd.Name] = fd
	}
	if !byName["host"].Required || byName["port"].Required {
		t.Error("required flags wrong")
	}
	if byName["host"].Title != "Host" || byName["mode"].Title != "mode" {
		t.Error("title fallback wrong")
	}
	if !byName["key"].Secret || byName["host"].Secret {
		t.Error("secret hint wrong")
	}
	if byName["port"].Type != "number" {
		t.Error("number type lost")
	}
}

func TestParseRefusesWhatItCannotRender(t *testing.T) {
	if _, err := Parse(`{"type":"array"}`); err == nil {
		t.Error("non-object top level must be refused")
	}
	if _, err := Parse(`{"type":"object","properties":{"x":{"type":"boolean"}}}`); err == nil {
		t.Error("unsupported field type must be refused (rendering wrong beats guessing)")
	}
	if _, err := Parse(`not json`); err == nil {
		t.Error("garbage must be refused")
	}
	// Unknown KEYS are tolerated — a newer plugin may say more than this
	// client understands; the plugin still validates authoritatively.
	if _, err := Parse(`{"type":"object","x-vendor":true,"properties":{"a":{"type":"string","maxLength":9}}}`); err != nil {
		t.Errorf("unknown keys must be ignored: %v", err)
	}
}

func TestValidateAndEncode(t *testing.T) {
	f, err := Parse(sshSchema)
	if err != nil {
		t.Fatal(err)
	}
	if errs := f.Validate(map[string]string{"user": "joe"}); len(errs) != 1 || !strings.Contains(errs[0], "Host") {
		t.Errorf("missing required: %v", errs)
	}
	if errs := f.Validate(map[string]string{"host": "h", "user": "j", "port": "abc"}); len(errs) != 1 || !strings.Contains(errs[0], "number") {
		t.Errorf("bad number: %v", errs)
	}
	if errs := f.Validate(map[string]string{"host": "h", "user": "j", "mode": "reckless"}); len(errs) != 1 || !strings.Contains(errs[0], "one of") {
		t.Errorf("bad enum: %v", errs)
	}

	out, err := f.Encode(map[string]string{"host": "rtb", "user": "joe", "port": "22", "mode": "fast"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"host":"rtb","mode":"fast","port":22,"user":"joe"}`
	if string(out) != want {
		t.Errorf("encoded = %s, want %s", out, want)
	}
	// Absent optional = absent key; encode of invalid values refuses.
	out, _ = f.Encode(map[string]string{"host": "h", "user": "j"})
	if strings.Contains(string(out), "port") {
		t.Errorf("absent optional leaked: %s", out)
	}
	if _, err := f.Encode(map[string]string{"user": "j"}); err == nil {
		t.Error("encode must refuse invalid values")
	}
}
