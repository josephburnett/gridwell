package plugin

import (
	"context"
	"strings"
	"testing"

	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
)

// FromConfig is the one config-to-plugin derivation: a missing pid takes the
// documented default of 1, and a pid that is not a positive integer is a
// refusal naming it, never a silent fallback to the whole process tree.
func TestFromConfigOwnsThePidDerivation(t *testing.T) {
	for _, bad := range []string{"abc", "0", "-3", "12x"} {
		if impl, err := FromConfig(map[string]string{"pid": bad}); err == nil || !strings.Contains(err.Error(), bad) {
			t.Errorf("pid %q → %v, %v; want a refusal naming it", bad, impl, err)
		}
	}
	for raw, want := range map[string]string{"": "1", "1": "1", " 4242 ": "4242"} {
		impl, err := FromConfig(map[string]string{"pid": raw})
		if err != nil {
			t.Fatalf("pid %q: %v", raw, err)
		}
		info, err := impl.(*Plugin).Info(context.Background(), &pluginv1.InfoRequest{})
		if err != nil || info.RootContext != want {
			t.Errorf("pid %q → root context %q, %v; want %q", raw, info.GetRootContext(), err, want)
		}
	}
}
