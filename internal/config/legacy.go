package config

// The pre-one-node server.yaml, converted at load.
//
// Before the one-node fold a file named `node_id:` and listed EVERY namespace
// under `plugins:` — the home store, the ssh transport and the content plugins
// alike, each with a `name:` and sometimes a `provider: true` marker. The fold
// made `plugins:` content plugins only and made the node's id its home's, and
// promised that a pre-one-node home converts itself at first serve. The
// database half of that promise is node.Convert; this is the config half, and
// it must run FIRST: Load's strict decode refuses every retired key, so
// without this the conversion never started at all — serve died on
// "field node_id not found" and node.Convert was never reached.
//
// One owner: the conversion happens here, inside Load, so every caller of the
// config — serve, backup, the desktop sidecar — sees the converted file and
// nothing else has to know the old shape. It runs once: the original is set
// aside as server.yaml.pre-one-node (never deleted), the converted file takes
// its place, and a file already in the new shape is not touched at all.
//
// Reading the legacy file's own `kind:` strings below is CONFIG MIGRATION, not
// plugin behavior: these names are the retired shape of THIS FILE, they are
// read in this one place, and nothing outside it switches on them. The host's
// rule — no host or client behavior derived from a plugin kind (CLAUDE.md,
// "plugins are the third-party door") — is untouched.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// legacyHomeKinds are the retired names of the node's own store. A row with
// one of these carries the id that IS the node's: node.Convert looks the home
// store up at db/<id>/store.db and the store verifies its stored identity
// against it, so the home row's id — never the old `node_id`, which named the
// deleted launcher grid — becomes `id:`.
var legacyHomeKinds = map[string]bool{"home": true, "local": true, "localdb": true}

// legacyTransportKinds are the retired names of the node's own transport. The
// node builds its transport from `connections:` now, so these rows are
// dropped. What such a row's store remembered — a connection's learned root
// grid, its tombstone — is re-derived: the root is re-learned on the first
// connect, and retirement is `retired_names:`. (A file from the last
// pre-one-node revision keeps the transport's rows in db/<home-id>/remote.db
// instead of a row of its own; node.Convert imports those.)
var legacyTransportKinds = map[string]bool{"remote": true, "ssh": true}

// legacyConfig is the union of every key a pre-one-node server.yaml could
// carry. The decode into it is strict too: an unknown key means a file shape
// this converter has never seen, and converting what it knows while silently
// dropping the rest is exactly the guess the guiding rule forbids.
type legacyConfig struct {
	NodeID        string              `yaml:"node_id"`
	Bind          string              `yaml:"bind"`
	Password      string              `yaml:"password"`
	Web           legacyWeb           `yaml:"web"`
	Federation    FederationConfig    `yaml:"federation"`
	StaticDir     string              `yaml:"static"`
	DisableShells bool                `yaml:"disable_shells"`
	Connections   []ConnectionConfig  `yaml:"connections"`
	RetiredNames  []string            `yaml:"retired_names"`
	Plugins       []legacyPluginEntry `yaml:"plugins"`
}

type legacyWeb struct {
	Bind     string `yaml:"bind"`
	Password string `yaml:"password"`
}

type legacyPluginEntry struct {
	ID       string            `yaml:"id"`
	Name     string            `yaml:"name"`
	Kind     string            `yaml:"kind"`
	Binary   string            `yaml:"binary"`
	Config   map[string]string `yaml:"config"`
	Provider bool              `yaml:"provider"`
}

// legacyProbe is the lenient pre-pass: the markers that say "this file is the
// old shape" without deciding anything else. A pointer for each optional
// marker, so `name: ""` — which is exactly what the old writer emitted for an
// unnamed row — still counts as present.
type legacyProbe struct {
	NodeID  string `yaml:"node_id"`
	Plugins []struct {
		Name     *string `yaml:"name"`
		Kind     string  `yaml:"kind"`
		Provider *bool   `yaml:"provider"`
	} `yaml:"plugins"`
}

// looksLegacy reports whether data is a pre-one-node file. The markers are
// exclusive to the old shape: `node_id:` is gone, a plugin row's `name:` is
// `label:`, `provider:` is gone, and a home or transport kind is refused by
// Parse — so a new-shape file can never trip this, and is never rewritten.
// A file this cannot parse at all is not legacy; Parse reports the syntax
// error with its line.
func looksLegacy(data []byte) bool {
	var p legacyProbe
	if err := yaml.Unmarshal(data, &p); err != nil {
		return false
	}
	if p.NodeID != "" {
		return true
	}
	for _, row := range p.Plugins {
		if row.Name != nil || row.Provider != nil {
			return true
		}
		if legacyHomeKinds[row.Kind] || legacyTransportKinds[row.Kind] {
			return true
		}
	}
	return false
}

// convertLegacy derives the one-node config from a pre-one-node file. It
// refuses rather than guesses: an unknown key, a missing home row, two home
// rows, a plugin row with no id or no kind, and a native row carrying config
// keys that have no home in the new shape all stop the conversion with the
// fix, leaving the file exactly as the user left it.
func convertLegacy(data []byte) (*ServerConfig, error) {
	var old legacyConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&old); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("this looks like a pre-one-node config, but it carries a key the converter has never seen: %w — convert it by hand (see docs/one-node.md)", err)
	}

	out := &ServerConfig{
		Federation:    old.Federation,
		StaticDir:     old.StaticDir,
		DisableShells: old.DisableShells,
		Connections:   old.Connections,
		RetiredNames:  old.RetiredNames,
	}
	// The doors: web.bind wins over the flat bind, which is how the
	// pre-2026-08-26 file said the same thing.
	out.Web.Bind = old.Web.Bind
	if out.Web.Bind == "" {
		out.Web.Bind = old.Bind
	}

	var home *legacyPluginEntry
	for i := range old.Plugins {
		row := &old.Plugins[i]
		if row.Kind == "" {
			return nil, fmt.Errorf("plugins[%d]: no kind — the converter cannot tell what this row is", i)
		}
		if row.ID == "" {
			return nil, fmt.Errorf("plugins[%d] (%s): no id — an id is immutable and cannot be minted over existing data", i, row.Kind)
		}
		switch {
		case legacyHomeKinds[row.Kind]:
			if home != nil {
				return nil, fmt.Errorf("two home rows (%s and %s) — only one can be the node's id", home.ID, row.ID)
			}
			home = row
		case legacyTransportKinds[row.Kind]:
			if err := noLeftoverConfig(row, nil); err != nil {
				return nil, err
			}
		default:
			out.Plugins = append(out.Plugins, PluginConfig{
				ID:     row.ID,
				Kind:   row.Kind,
				Label:  row.Name, // `name:` is `label:`; `provider:` is dropped
				Binary: row.Binary,
				Config: row.Config,
			})
		}
	}
	if home == nil {
		return nil, errors.New("no home row (kind home/local/localdb) — the node's id is the home store's id, and minting a fresh one would orphan every stored reference; add the row back, or set `id:` by hand to the id under db/")
	}
	out.ID = home.ID
	// The home row's one durable config key: the login shell, which is
	// `shell:` at the top level now.
	out.Shell = home.Config["shell"]
	if err := noLeftoverConfig(home, map[string]bool{"shell": true}); err != nil {
		return nil, err
	}
	return out, nil
}

// noLeftoverConfig refuses a native row whose config carries keys the new
// shape has nowhere to put. db_file was always derived, never a user fact, so
// a file that names it is not ambiguous.
func noLeftoverConfig(row *legacyPluginEntry, known map[string]bool) error {
	var left []string
	for k := range row.Config {
		if known[k] || k == "db_file" {
			continue
		}
		left = append(left, k)
	}
	if len(left) == 0 {
		return nil
	}
	sort.Strings(left)
	return fmt.Errorf("the %s row carries config keys with no home in the one-node config: %v — a connection's fields are `connections:` rows now; remove them (or the whole row) and serve again", row.Kind, left)
}

// convertFile is the file-level half: derive the new config, check it loads,
// set the original aside as server.yaml.pre-one-node, and write the converted
// file in its place with the same atomic 0600 write every config write uses.
// It returns the converted bytes, which Load then parses as if they had always
// been there.
func convertFile(path string, data []byte) ([]byte, error) {
	cfg, err := convertLegacy(data)
	if err != nil {
		return nil, err
	}
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal the converted config: %w", err)
	}
	// The converted file must load through the one door, so a conversion can
	// never write a file the next boot refuses.
	if _, err := Parse(out); err != nil {
		return nil, fmt.Errorf("the converted config does not load: %w", err)
	}
	aside := path + ".pre-one-node"
	if _, err := os.Stat(aside); err == nil {
		return nil, fmt.Errorf("%s already exists — the original of an earlier conversion is never overwritten; move it away and serve again", aside)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.Rename(path, aside); err != nil {
		return nil, fmt.Errorf("set the old config aside: %w", err)
	}
	if err := writeFileAtomic(path, out); err != nil {
		return nil, err
	}
	log.Printf("gridwell: converted %s to the one-node shape: id %s (the home's), %d content plugin(s); the original is %s (delete when satisfied)",
		path, cfg.ID, len(cfg.Plugins), aside)
	if old := legacyPasswordNote(data); old != "" {
		log.Printf("gridwell: %s", old)
	}
	return out, nil
}

// legacyPasswordNote reports the one legacy key whose value is deliberately
// dropped rather than carried, so the user is told instead of wondering.
func legacyPasswordNote(data []byte) string {
	var old legacyConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&old); err != nil {
		return ""
	}
	if old.Password == "" && old.Web.Password == "" {
		return ""
	}
	return "the old config's web password is not carried: the password is the web-password file beside server.yaml, printed at serve (delete it to rotate)"
}
