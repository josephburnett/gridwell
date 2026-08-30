package local

import (
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/pluginmeta"
)

// OpenVerified is the one door the home's store is opened through: verify
// the DB's stored identity against the configured id and kind
// (pluginmeta.Verify), open the store, and inject the verified config id
// (store.SetPluginID) so every identity read — the boot scratch sweep's
// WorkspaceEphemeralRefs comparison included — speaks the id that qualified
// references actually carry. Fusing verify, open, and inject into one call
// makes the forgotten-injection state unrepresentable.
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
