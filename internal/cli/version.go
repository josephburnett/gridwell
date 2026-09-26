package cli

import (
	"runtime"

	"github.com/josephburnett/gridwell/api/tracewire"
)

// Version is stamped by the release build from the git tag, its one owner.
var Version = ""

// VersionString never returns empty: an unstamped binary is honestly "dev"
// rather than a drifted number.
func VersionString() string {
	if Version == "" {
		return "dev"
	}
	return Version
}

// bootRecord is the node's first trace record: the build and the home it
// serves.
func bootRecord(home string) tracewire.Record {
	kv := map[string]string{"version": VersionString(), "go": runtime.Version(), "home": home}
	if c := tracewire.BuildCommit(); c != "" {
		kv["commit"] = c
	}
	return tracewire.Record{Origin: tracewire.OriginNode, Src: "node", Kind: tracewire.KindBoot,
		Msg: "gridwell " + VersionString(), KV: kv}
}
