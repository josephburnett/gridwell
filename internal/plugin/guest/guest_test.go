package guest

import (
	"reflect"
	"testing"

	gplug "github.com/josephburnett/gridwell/internal/plugin"
)

// TestConfigDecodesEnv: a JSON object in GRIDWELL_PLUGIN_CONFIG decodes to the
// config map the plugin reads at spawn (db_file, uuid, kind, …).
func TestConfigDecodesEnv(t *testing.T) {
	t.Setenv(gplug.ConfigEnvVar, `{"db_file":"/x/store.db","uuid":"abc","kind":"localdb"}`)
	got := Config()
	want := map[string]string{"db_file": "/x/store.db", "uuid": "abc", "kind": "localdb"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Config() = %v, want %v", got, want)
	}
}

// TestConfigEmptyWhenUnset: an unset/empty env yields an empty (non-nil) map,
// so callers can index it without a nil check.
func TestConfigEmptyWhenUnset(t *testing.T) {
	t.Setenv(gplug.ConfigEnvVar, "")
	got := Config()
	if got == nil || len(got) != 0 {
		t.Errorf("Config() with unset env = %v, want empty non-nil map", got)
	}
}

// TestConfigMalformedYieldsEmpty: malformed JSON does not panic or propagate;
// it degrades to an empty map (the plugin then fails its own identity Verify
// with a clear error rather than crashing on startup).
func TestConfigMalformedYieldsEmpty(t *testing.T) {
	t.Setenv(gplug.ConfigEnvVar, `{not valid json`)
	got := Config()
	if got == nil || len(got) != 0 {
		t.Errorf("Config() with malformed env = %v, want empty non-nil map", got)
	}
}
