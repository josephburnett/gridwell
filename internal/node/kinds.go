package node

import "fmt"

// RenamedKinds maps every RETIRED kind name to its current one (owner
// decisions 2026-08-16 and 2026-08-27: "localdb" and "local" → "home",
// the server's own DB; "ssh" → "remote"). A retired name never returns:
// Init refuses it before writing anything, and the CLI's one-shot
// migration (internal/cli/kindmigrate.go, DELETE AFTER 2026-09-27)
// reads this same map to rewrite an old server.yaml at serve boot. One
// owner for the fact, so init and the migration can never disagree
// about which names are gone.
var RenamedKinds = map[string]string{
	"localdb": "home",
	"local":   "home",
	"ssh":     "remote",
}

// checkKind refuses a retired kind name, naming the current one.
func checkKind(kind string) error {
	if current, retired := RenamedKinds[kind]; retired {
		return fmt.Errorf("kind %q is retired; use --kind %s", kind, current)
	}
	return nil
}
