package local

import (
	"github.com/josephburnett/gridwell/internal/local/store"
	"github.com/josephburnett/gridwell/internal/pluginmeta"
)

// OpenVerified is the one way the home's store is opened: verify the
// DB's stored identity against the configured id and kind, open the store, and
// inject the verified config id, so every identity read speaks the id that
// qualified references carry. Fusing the three makes the forgotten-injection
// state unrepresentable.
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
