package local

import "github.com/josephburnett/gridwell/internal/local/store"

// Store exposes the plugin's store to the external tests (writes that
// bypass the wire on purpose). Test-only: production reaches the store
// through the service methods alone.
func (p *Plugin) Store() *store.Store { return p.st }
