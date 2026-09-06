package connection

import (
	"fmt"

	"github.com/josephburnett/gridwell/internal/local/store"
)

// OpenDB opens a node store of its own at path and binds the connection store
// to its handle: the test-only shape. Production shares the node's one handle
// through NewDB. It goes through store.Open because the store owns the
// connections table's DDL — there is no other way to make a file that has one.
// DB.Close then closes that handle, which is all Store.Close does.
func OpenDB(path string) (*DB, error) {
	st, err := store.Open(path)
	if err != nil {
		return nil, fmt.Errorf("connection: open %q: %w", path, err)
	}
	d, err := NewDB(st.SQL())
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	d.owned = true
	return d, nil
}
