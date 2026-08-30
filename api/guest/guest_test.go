package guest

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	gplug "github.com/josephburnett/gridwell/api/compose"
	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
)

// TestConfigDecodesEnv: a JSON object in GRIDWELL_PLUGIN_CONFIG decodes to
// the config map the plugin reads at spawn.
func TestConfigDecodesEnv(t *testing.T) {
	t.Setenv(gplug.ConfigEnvVar, `{"db_file":"/x/store.db","uuid":"abc","kind":"home"}`)
	got, err := Config()
	want := map[string]string{"db_file": "/x/store.db", "uuid": "abc", "kind": "home"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("Config() = %v, want %v", got, want)
	}
}

// TestConfigEmptyWhenUnset: an unset or empty env yields an empty non-nil
// map, so callers can index it without a nil check.
func TestConfigEmptyWhenUnset(t *testing.T) {
	t.Setenv(gplug.ConfigEnvVar, "")
	got, err := Config()
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("Config() with unset env = %v, want empty non-nil map", got)
	}
}

// TestConfigMalformedIsAnError: a value that is not a JSON object is an
// error naming the variable — never an empty map, which would run the
// plugin unconfigured (fs rootless, proc at pid 1) as if that were what
// server.yaml said.
func TestConfigMalformedIsAnError(t *testing.T) {
	t.Setenv(gplug.ConfigEnvVar, `{not valid json`)
	got, err := Config()
	if err == nil || !strings.Contains(err.Error(), gplug.ConfigEnvVar) {
		t.Errorf("Config() with malformed env = %v, %v; want an error naming %s", got, err, gplug.ConfigEnvVar)
	}
}

// TestMainRefusesTheHandshake: a config that will not decode, and a factory
// that refuses its config, both become a plugin whose Info answers
// FailedPrecondition with the reason, which is how the host stops the
// launch naming it. A factory that builds is served as itself.
func TestMainRefusesTheHandshake(t *testing.T) {
	ctx := context.Background()
	t.Setenv(gplug.ConfigEnvVar, `{not valid json`)
	impl := build(func(map[string]string) (pluginv1.PluginServer, error) {
		t.Fatal("the factory must not run on an undecodable config")
		return nil, nil
	})
	if _, err := impl.Info(ctx, &pluginv1.InfoRequest{}); status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), gplug.ConfigEnvVar) {
		t.Errorf("undecodable config → Info %v, want FailedPrecondition naming %s", err, gplug.ConfigEnvVar)
	}

	t.Setenv(gplug.ConfigEnvVar, `{"pid":"abc"}`)
	var seen map[string]string
	impl = build(func(cfg map[string]string) (pluginv1.PluginServer, error) {
		seen = cfg
		return nil, errors.New("pid abc is not a process id")
	})
	if seen["pid"] != "abc" {
		t.Errorf("factory saw %v, want the decoded config", seen)
	}
	if _, err := impl.Info(ctx, &pluginv1.InfoRequest{}); status.Code(err) != codes.FailedPrecondition || !strings.Contains(err.Error(), "not a process id") {
		t.Errorf("refusing factory → Info %v, want FailedPrecondition with its reason", err)
	}

	built := &pluginv1.UnimplementedPluginServer{}
	if got := build(func(map[string]string) (pluginv1.PluginServer, error) { return built, nil }); got != built {
		t.Errorf("a factory that builds must be served as itself, got %T", got)
	}
}
