package local

import (
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/pluginmeta"
)

// OpenVerified is the ONE door a localdb binary opens its store through:
// verify the DB's stored identity against the configured id+kind
// (pluginmeta.Verify), open the store, and inject the verified config id
// (store.SetPluginID) so every identity read — including the boot scratch
// sweep's WorkspaceEphemeralRefs comparison — speaks the id that qualified
// references actually carry. Issue #196: the store-side injection existed
// but main.go never called it, so production identity silently fell back
// to the bootstrap-minted system.plugin_uuid; fusing verify+open+inject
// here makes the forgotten-injection state unrepresentable.
func OpenVerified(dbPath, uuid, kind string) (*store.Store, error) {
	if _, err := pluginmeta.Verify(dbPath, uuid, kind); err != nil {
		return nil, err
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	st.SetPluginID(uuid)
	return st, nil
}
