package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"strconv"

	"gopkg.in/yaml.v3"

	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/plugin/sshhost"
)

// sshConnKeys are the retired config-pinned mount keys (pre-#199 shape,
// removed with #251): a connection described in server.yaml instead of as
// data. remote_plugin is the even older pre-federation key, stripped along
// with the rest.
var sshConnKeys = []string{"host", "user", "port", "key", "known_hosts", "addr", "remote_plugin"}

// migrateSSHConfigConnections is the #251 config→data migration, run at
// serve boot before anything spawns: an ssh plugin entry still carrying
// connection config keys has that connection IMPORTED into the plugin's own
// DB as a named connection row (the config `name` becomes the connection's
// user-owned name), and server.yaml is rewritten without the keys — config
// migrated and kept up to date, the same contract as a schema migration.
//
// Idempotent two ways: the keys are gone after the rewrite, and an import
// whose canonical params already match a live connection is skipped — so a
// crash between import and rewrite cannot mint twins on the next boot.
// Operates on the RAW yaml (like config.AppendPlugin), never on the
// path-expanded Load view, so untouched entries stay byte-identical.
func migrateSSHConfigConnections(home, cfgPath string) error {
	data, err := os.ReadFile(cfgPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // no config yet; serve will say so with its own message
	}
	if err != nil {
		return err
	}
	var cfg config.ServerConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse %s: %w", cfgPath, err)
	}
	changed := false
	for i := range cfg.Plugins {
		pc := &cfg.Plugins[i]
		if pc.Kind != "ssh" || pc.Config["host"] == "" {
			continue
		}
		if err := importSSHConnection(home, pc); err != nil {
			return fmt.Errorf("migrate ssh plugin %q: %w", pc.Name, err)
		}
		for _, k := range sshConnKeys {
			delete(pc.Config, k)
		}
		if len(pc.Config) == 0 {
			pc.Config = nil
		}
		changed = true
		fmt.Printf("gridwell: migrated ssh config %q into a connection (connections are data — #251)\n", pc.Name)
	}
	if !changed {
		return nil
	}
	out, err := yaml.Marshal(&cfg)
	if err != nil {
		return err
	}
	tmp := cfgPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, cfgPath)
}

// importSSHConnection writes one config-described connection into the ssh
// plugin's DB: params from the config keys (the `host` key was "host:port"),
// the plugin's configured name as the connection's user-owned name.
func importSSHConnection(home string, pc *config.PluginConfig) error {
	ctx := context.Background()
	hostport := pc.Config["host"]
	host, portStr, err := net.SplitHostPort(hostport)
	if err != nil {
		host, portStr = hostport, "" // bare host: the dial default (22) applies
	}
	doc := map[string]any{"host": host}
	if portStr != "" {
		p, err := strconv.Atoi(portStr)
		if err != nil {
			return fmt.Errorf("port %q: %w", portStr, err)
		}
		doc["port"] = p
	}
	for _, k := range []string{"user", "key", "known_hosts", "addr"} {
		if v := pc.Config[k]; v != "" {
			doc[k] = v
		}
	}
	params, err := json.Marshal(doc) // map keys sort — a stable params document
	if err != nil {
		return err
	}
	if _, err := sshhost.ParseParams(params); err != nil {
		return err
	}
	dbFile := config.DBFile(home, pc.ID)
	if _, err := os.Stat(dbFile); err != nil {
		return fmt.Errorf("no database at %s (run `gridwell init` first): %w", dbFile, err)
	}
	db, err := sshhost.OpenDB(dbFile)
	if err != nil {
		return err
	}
	defer db.Close()

	// Skip when the connection already exists (a crash between import and
	// the yaml rewrite must not mint a twin — the #251 dedup rule).
	want, err := sshhost.CanonicalParams(params)
	if err != nil {
		return err
	}
	live, err := db.List(ctx)
	if err != nil {
		return err
	}
	x := int64(0)
	for _, c := range live {
		if c.Params != "" {
			if got, cerr := sshhost.CanonicalParams([]byte(c.Params)); cerr == nil && got == want {
				return nil
			}
		}
		if c.X+c.W > x {
			x = c.X + c.W
		}
	}
	conn, err := db.Create(ctx, x, 0, 1, 1, "")
	if err != nil {
		return err
	}
	// The configured name is user-chosen: latch it (Rename) so ssh's
	// automatic user@host label never overwrites it.
	if pc.Name != "" {
		if conn, err = db.Rename(ctx, conn.ID, conn.Version, pc.Name); err != nil {
			return err
		}
	}
	if _, err := db.SetParams(ctx, conn.ID, conn.Version, string(params)); err != nil {
		return err
	}
	return db.BumpGridVersion(ctx)
}
