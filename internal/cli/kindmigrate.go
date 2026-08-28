package cli

// ── DELETE AFTER 2026-09-27 ─────────────────────────────────────────────
// One-shot kind renames (owner decisions 2026-08-16 and 2026-08-27):
// "localdb" → "home", "local" → "home" (the server's own DB is "home"),
// "ssh" → "remote". Runs at serve boot: rewrites server.yaml kinds and
// each affected plugin DB's identity stamp (pluginmeta.UpdateKind — ids
// never change), then never matches again. Single-user migration window;
// tracked for removal by the dated GitHub issue. After deletion the old
// names are simply unknown kinds (no binary resolves), which is the
// correct permanent behavior.

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/josephburnett/gridwell/api/pluginmeta"
	"github.com/josephburnett/gridwell/internal/config"
)

var renamedKinds = map[string]string{
	"localdb": "home",
	"local":   "home",
	"ssh":     "remote",
}

// migrateRenamedKinds updates retired kind names in server.yaml and the
// corresponding DB stamps. A missing config is not an error (serve says
// so itself); nothing to rename is the steady state.
func migrateRenamedKinds(home, cfgPath string) error {
	data, err := os.ReadFile(cfgPath)
	if os.IsNotExist(err) {
		return nil
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
		newKind, ok := renamedKinds[pc.Kind]
		if !ok {
			continue
		}
		if err := pluginmeta.UpdateKind(config.DBFile(home, pc.ID), newKind); err != nil {
			return fmt.Errorf("restamp plugin %q (%s) kind: %w", pc.Name, pc.ID, err)
		}
		pc.Kind = newKind
		changed = true
		fmt.Printf("gridwell: migrated plugin %q kind to %q (one-shot rename, 2026-08-16)\n", pc.Name, newKind)
	}
	if !changed {
		return nil
	}
	out, err := yaml.Marshal(&cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(cfgPath, out, 0o600)
}
