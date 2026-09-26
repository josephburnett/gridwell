package cli

import (
	"runtime"
	"testing"

	"github.com/josephburnett/gridwell/api/tracewire"
)

// An unstamped binary is honestly "dev", never empty: a blank line would
// read as a broken command.
func TestVersionStringIsNeverEmpty(t *testing.T) {
	saved := Version
	t.Cleanup(func() { Version = saved })

	Version = ""
	if got := VersionString(); got != "dev" {
		t.Errorf("unstamped VersionString() = %q, want \"dev\"", got)
	}
	Version = "0.1.0"
	if got := VersionString(); got != "0.1.0" {
		t.Errorf("stamped VersionString() = %q, want the stamp verbatim", got)
	}
}

// The node's first record names the build that wrote every record after it
// and the home it serves, so a dump says which binary it came from.
func TestTheBootRecordNamesTheBuild(t *testing.T) {
	rec := bootRecord("/h/gridwell")
	if rec.Origin != tracewire.OriginNode || rec.Src != "node" || rec.Kind != tracewire.KindBoot {
		t.Errorf("the boot record is %s/%s/%s", rec.Origin, rec.Src, rec.Kind)
	}
	if rec.KV["version"] != VersionString() || rec.KV["go"] != runtime.Version() || rec.KV["home"] != "/h/gridwell" {
		t.Errorf("the boot record's kv is %v", rec.KV)
	}
	// go test stamps no commit, and an absent key is not an empty one.
	if c, ok := rec.KV["commit"]; ok && c == "" {
		t.Errorf("the boot record carries an empty commit")
	}
}
