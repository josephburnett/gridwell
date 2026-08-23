package cli

// gridwell convert-v2 — the one-time home migration (docs/v2-design.md
// §8): OFFLINE (the source server must be stopped), NEVER in place (the
// source home is opened read-only; --to must not exist), identity
// verbatim. What actually converts, per the executed design:
//
//   - local entries: the native store — the DB copies VERBATIM (the fold
//     made it node code; the file and every id inside are unchanged).
//   - remote entries: the transport DB copies verbatim, and its rows EMIT
//     the server.yaml `connections:` + `retired_names:` sections (yaml
//     becomes the owner; the first boot reconciles as a no-op).
//   - fs / proc entries: the legacy plugin DB converts into the v2
//     external memory DB (ids and sequences verbatim) and the entry gains
//     `provider: true`.
//   - node-view.json and the rest of the home's state files copy along;
//     the cache/ dir does not (disposable by contract; it re-warms).
//
// Then verify before cutover: run the CURRENT binary against the OLD
// home (legacy mode) and against the NEW home, and `gridwell parity`
// them to zero differences. The old home is the rollback until the
// first write in the new world.

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/josephburnett/gridwell/internal/config"
	"github.com/josephburnett/gridwell/internal/convert"
)

// RunConvertV2 implements `gridwell convert-v2 --from HOME --to HOME`.
func RunConvertV2(args []string) int {
	fs := flag.NewFlagSet("convert-v2", flag.ContinueOnError)
	from := fs.String("from", "", "the OLD home (read-only; its server must be stopped)")
	to := fs.String("to", "", "the NEW home to create (must not exist)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *from == "" || *to == "" {
		fmt.Fprintln(os.Stderr, "convert-v2: --from and --to are required")
		return 2
	}
	if err := convertV2(*from, *to); err != nil {
		fmt.Fprintf(os.Stderr, "convert-v2: %v\n", err)
		return 1
	}
	fmt.Printf(`convert-v2: done — %s is the v2 home.
Verify BEFORE cutover (the old home is untouched and remains the rollback):
  GRIDWELL_HOME=%s gridwell serve --bind 127.0.0.1:39001 &
  GRIDWELL_HOME=%s gridwell serve --bind 127.0.0.1:39002 &
  gridwell parity --a http://127.0.0.1:39001 --b http://127.0.0.1:39002 \
      --scope %s/convert-scope.txt [--password ...]
Zero differences or no cutover.
`, *to, *from, *to, *to)
	return 0
}

func convertV2(from, to string) error {
	if _, err := os.Stat(to); err == nil {
		return fmt.Errorf("%s already exists — convert-v2 never writes into an existing home", to)
	}
	if _, held, _ := probeServeLock(from); held {
		return fmt.Errorf("a server is running on %s — stop it first (the conversion needs a quiescent source)", from)
	}
	cfg, err := config.Load(filepath.Join(from, "server.yaml"))
	if err != nil {
		return err
	}
	if cfg.ConnectionsSet {
		return fmt.Errorf("%s already carries a connections: key — it looks converted already", from)
	}
	if err := os.MkdirAll(filepath.Join(to, "db"), 0o755); err != nil {
		return err
	}

	// The parity scope: whole namespaces for the finite stores, explicit
	// grid lists for the unbounded projections (an unscoped crawl over a
	// live filesystem would materialize the world on both sides).
	scope := []string{"# convert-v2 parity scope — pass to `gridwell parity --scope`"}
	if cfg.NodeID != "" {
		scope = append(scope, "ns:"+cfg.NodeID)
	}

	for i := range cfg.Plugins {
		pc := &cfg.Plugins[i]
		srcDB := config.DBFile(from, pc.ID)
		dstDir := config.DBDir(to, pc.ID)
		if err := os.MkdirAll(dstDir, 0o755); err != nil {
			return err
		}
		dstDB := config.DBFile(to, pc.ID)
		switch {
		case pc.Provider:
			return fmt.Errorf("plugin %q (%s) is already a provider entry — %s looks converted already", pc.Name, pc.ID, from)
		case pc.Kind == "local" || pc.Kind == "remote":
			scope = append(scope, "ns:"+pc.ID)
			// The folds left these files exactly as they were: copy
			// verbatim (plus WAL/SHM siblings if the source wasn't
			// checkpointed).
			for _, suffix := range []string{"", "-wal", "-shm"} {
				if err := copyFile(srcDB+suffix, dstDB+suffix); err != nil {
					return fmt.Errorf("plugin %q (%s): %w", pc.Name, pc.ID, err)
				}
			}
			if pc.Kind == "remote" {
				conns, retired, err := convert.Connections(srcDB, pc.ID)
				if err != nil {
					return err
				}
				cfg.Connections = append(cfg.Connections, conns...)
				cfg.RetiredNames = append(cfg.RetiredNames, retired...)
			}
		case pc.Kind == "fs":
			root := pc.Config["root"]
			if root == "" {
				return fmt.Errorf("plugin %q (%s): fs entry has no root — cannot derive keys", pc.Name, pc.ID)
			}
			res, err := convert.FS(srcDB, dstDB, pc.ID, pc.Kind, root)
			if err != nil {
				return err
			}
			fmt.Printf("convert-v2: fs %s: %d grids, %d tiles\n", pc.ID, res.Grids, res.Tiles)
			for _, gid := range res.GridIDs {
				scope = append(scope, fmt.Sprintf("%s/%d", pc.ID, gid))
			}
			pc.Provider = true
		case pc.Kind == "proc":
			res, err := convert.Proc(srcDB, dstDB, pc.ID, pc.Kind)
			if err != nil {
				return err
			}
			fmt.Printf("convert-v2: proc %s: %d grids, %d tiles\n", pc.ID, res.Grids, res.Tiles)
			for _, gid := range res.GridIDs {
				scope = append(scope, fmt.Sprintf("%s/%d", pc.ID, gid))
			}
			pc.Provider = true
		default:
			// An unknown kind is somebody's third-party plugin: its DB is
			// theirs; copy it verbatim and leave the entry alone.
			for _, suffix := range []string{"", "-wal", "-shm"} {
				if err := copyFile(srcDB+suffix, dstDB+suffix); err != nil {
					return fmt.Errorf("plugin %q (%s): %w", pc.Name, pc.ID, err)
				}
			}
		}
		// db_file was injected by Load-side derivation only in serve paths;
		// nothing to persist.
		delete(pc.Config, "db_file")
		if len(pc.Config) == 0 {
			pc.Config = nil
		}
	}

	// The connections key becomes PRESENT even when empty: yaml is the
	// owner from here on (config mode).
	if cfg.Connections == nil {
		cfg.Connections = []config.ConnectionConfig{}
	}

	// State files ride along (node-view.json — the launcher arrangement
	// and viewport); cache/ and serve.lock do not.
	entries, err := os.ReadDir(from)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || e.Name() == "server.yaml" || e.Name() == "serve.lock" {
			continue
		}
		if err := copyFile(filepath.Join(from, e.Name()), filepath.Join(to, e.Name())); err != nil {
			return err
		}
	}

	if err := os.WriteFile(filepath.Join(to, "convert-scope.txt"),
		[]byte(strings.Join(scope, "\n")+"\n"), 0o600); err != nil {
		return err
	}

	out, err := marshalConfigV2(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(to, "server.yaml"), out, 0o600)
}

// marshalConfigV2 serializes the converted config. An EMPTY connections
// list must still serialize its key (presence = config mode), which
// omitempty would drop — a wrapper without the omitempty carries it.
func marshalConfigV2(cfg *config.ServerConfig) ([]byte, error) {
	type v2cfg struct {
		NodeID        string                    `yaml:"node_id,omitempty"`
		Bind          string                    `yaml:"bind,omitempty"`
		StaticDir     string                    `yaml:"static,omitempty"`
		Password      string                    `yaml:"password,omitempty"`
		DisableShells bool                      `yaml:"disable_shells,omitempty"`
		Plugins       []config.PluginConfig     `yaml:"plugins"`
		Connections   []config.ConnectionConfig `yaml:"connections"`
		RetiredNames  []string                  `yaml:"retired_names,omitempty"`
	}
	bind := cfg.Bind
	if !cfg.BindSet {
		bind = "" // the default was filled by Load; don't pin it
	}
	return yaml.Marshal(&v2cfg{
		NodeID: cfg.NodeID, Bind: bind, StaticDir: cfg.StaticDir,
		Password: cfg.Password, DisableShells: cfg.DisableShells,
		Plugins: cfg.Plugins, Connections: cfg.Connections, RetiredNames: cfg.RetiredNames,
	})
}

// copyFile copies src to dst (0600); a missing src is skipped only for
// WAL/SHM siblings — the caller passes those explicitly.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
