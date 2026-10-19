package cli

import (
	"runtime"
	"strconv"

	"github.com/josephburnett/gridwell/api/tracewire"
	"github.com/josephburnett/gridwell/internal/local/store"
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

// bootRecord is the node's first trace record: the build, the gridwell.db
// schema it opens (a newer file is refused, an older one migrated to it) and
// the directory it serves.
func bootRecord(dir string) tracewire.Record {
	kv := map[string]string{"version": VersionString(), "go": runtime.Version(), "dir": dir,
		"schema": strconv.Itoa(store.SchemaVersion())}
	msg := "gridwell " + VersionString()
	if c := tracewire.BuildCommit(); c != "" {
		kv["commit"] = c
		msg += " " + tracewire.ShortCommit(c)
	}
	return tracewire.Record{Origin: tracewire.OriginNode, Src: "node", Kind: tracewire.KindBoot,
		Msg: msg, KV: kv}
}
