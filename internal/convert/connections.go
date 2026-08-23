package convert

// Connections emits the server.yaml `connections:` section from a legacy
// transport DB (v2 #269: yaml becomes the owner; the DB remains the
// node-side materialization, so the first config-mode boot reconciles to
// a no-op). Live rows become declarations; tombstoned rows become
// retired_names — the graveyard travels, so no name ever returns.

import (
	"database/sql"
	"fmt"

	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/remote"
	_ "modernc.org/sqlite"
)

// remoteKnownTables is the transport DB surface this converter
// understands (plus pluginmeta's table).
var remoteKnownTables = map[string][]string{
	"_gridwell_meta":  {"k", "v"},
	"ssh_connections": {"id", "ns", "object_id", "params", "version", "x", "y", "w", "h", "alt_text", "alt_user", "view_x", "view_y", "view_zoom", "remote_root", "deleted"},
	"ssh_meta":        {"k", "v"},
	"sqlite_sequence": nil,
}

// Connections reads a legacy transport DB and returns the yaml
// declarations plus the retired-name graveyard. A live row whose params
// never committed (a picker stub that never connected) is REFUSED — the
// operator deletes it or finishes it; nothing is silently dropped.
func Connections(legacyPath, uuid string) ([]config.ConnectionConfig, []string, error) {
	src, err := sql.Open("sqlite", "file:"+legacyPath+"?mode=ro")
	if err != nil {
		return nil, nil, fmt.Errorf("convert connections: open %s: %w", legacyPath, err)
	}
	defer src.Close()
	src.SetMaxOpenConns(1)
	if err := refuseUnknown(src, "connections", remoteKnownTables); err != nil {
		return nil, nil, err
	}
	if err := verifyMeta(src, uuid, "remote"); err != nil {
		return nil, nil, err
	}

	rows, err := src.Query(`SELECT ns, params, alt_text, deleted FROM ssh_connections ORDER BY id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []config.ConnectionConfig
	var retired []string
	for rows.Next() {
		var ns, params, alt string
		var deleted int64
		if err := rows.Scan(&ns, &params, &alt, &deleted); err != nil {
			return nil, nil, err
		}
		if deleted != 0 {
			retired = append(retired, ns)
			continue
		}
		if params == "" {
			return nil, nil, fmt.Errorf("convert connections: %q has no committed params (a picker stub that never connected) — delete it or finish it before converting", ns)
		}
		p, err := remote.ParseParams([]byte(params))
		if err != nil {
			return nil, nil, fmt.Errorf("convert connections: %q: %w", ns, err)
		}
		cc := config.ConnectionConfig{
			Name:       ns,
			Host:       p.Host,
			User:       p.User,
			Port:       int64(p.Port),
			Addr:       p.Addr,
			Key:        p.Key,
			KnownHosts: p.KnownHosts,
		}
		// The label rides only when it isn't the derivable default.
		if alt != "" && alt != p.User+"@"+p.Host {
			cc.Label = alt
		}
		out = append(out, cc)
	}
	return out, retired, rows.Err()
}

// ConnSpecs converts yaml declarations to the transport's vocabulary
// (the same mapping the serve wiring performs).
func ConnSpecs(conns []config.ConnectionConfig) []remote.ConnSpec {
	out := make([]remote.ConnSpec, len(conns))
	for i, c := range conns {
		out[i] = remote.ConnSpec{Name: c.Name, Label: c.Label, Host: c.Host, User: c.User,
			Port: c.Port, Addr: c.Addr, Key: c.Key, KnownHosts: c.KnownHosts}
	}
	return out
}
