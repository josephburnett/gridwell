package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// laptopYAML is a genuine pre-one-node server.yaml, the shape a home written
// before the fold still has on disk: `node_id:`, a `kind: home` row carrying
// the id that IS the node's, content rows with `name:` and the retired
// `provider:` flag, and `retired_names:` at the top level. Only the paths are
// changed. This exact file is what serve refused with "field node_id not found
// in type config.ServerConfig", which is what kept node.Convert from ever
// running.
const laptopYAML = `node_id: 8aed3340244e2053890889c4759cd373
plugins:
    - id: 52f8374fa356402c66e41b8097341b09
      name: ""
      kind: home
    - id: fa21d5d19ab177018f1f11c7357d6ffc
      name: ""
      kind: fs
      config:
        root: /
      provider: true
    - id: ngkwanw
      name: gitlab
      kind: gitlab
      config:
        token_file: /opt/pat
retired_names:
    - eoifgyl
    - phckk2d
`

func writeYAML(t *testing.T, dir, yml string) string {
	t.Helper()
	path := filepath.Join(dir, "server.yaml")
	if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The upgrade path: a pre-one-node file loads, because it converts itself
// first. The node's id is the HOME ROW's, never the old node_id — node.Convert
// looks the home store up at db/<id>/store.db and the store verifies its own
// stored identity against it, so any other id would refuse to open the user's
// data (or, worse, mint a fresh empty home beside it).
func TestLoadConvertsAPreOneNodeConfig(t *testing.T) {
	dir := t.TempDir()
	path := writeYAML(t, dir, laptopYAML)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ID != "52f8374fa356402c66e41b8097341b09" {
		t.Fatalf("id = %q, want the HOME row's id (not node_id)", cfg.ID)
	}
	if len(cfg.Plugins) != 2 {
		t.Fatalf("plugins = %+v, want the two content rows only (the home row is the node)", cfg.Plugins)
	}
	fs, gl := cfg.Plugins[0], cfg.Plugins[1]
	if fs.ID != "fa21d5d19ab177018f1f11c7357d6ffc" || fs.Kind != "fs" || fs.Label != "" || fs.Config["root"] != "/" {
		t.Errorf("fs row = %+v", fs)
	}
	if gl.ID != "ngkwanw" || gl.Kind != "gitlab" || gl.Label != "gitlab" || gl.Config["token_file"] != "/opt/pat" {
		t.Errorf("gitlab row = %+v (name becomes label)", gl)
	}
	if len(cfg.RetiredNames) != 2 || cfg.RetiredNames[0] != "eoifgyl" || cfg.RetiredNames[1] != "phckk2d" {
		t.Errorf("retired names = %v, want both carried over", cfg.RetiredNames)
	}
	if cfg.Web.BindSet || cfg.Web.Bind != Defaults.Web.Bind {
		t.Errorf("web = %+v, want the built-in default (the old file named no bind)", cfg.Web)
	}

	// The original is set aside, byte for byte, and never deleted.
	aside, err := os.ReadFile(path + ".pre-one-node")
	if err != nil {
		t.Fatalf("the original must be set aside: %v", err)
	}
	if string(aside) != laptopYAML {
		t.Error("the set-aside original must be the user's file, unmodified")
	}
	// The file in place is the new shape, 0600, with no retired key left.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("converted config mode = %v, want 0600", info.Mode().Perm())
	}
	converted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"node_id", "provider", "name:", "kind: home"} {
		if strings.Contains(string(converted), gone) {
			t.Errorf("converted config still names %q:\n%s", gone, converted)
		}
	}

	// Idempotent: the converted file is already the new shape, so a second
	// load rewrites nothing and the set-aside original stays put.
	if _, err := Load(path); err != nil {
		t.Fatalf("second Load: %v", err)
	}
	again, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(converted) {
		t.Error("a second load must leave the converted file byte-for-byte the same")
	}
	if _, err := os.Stat(path + ".pre-one-node.pre-one-node"); !os.IsNotExist(err) {
		t.Error("a second load must not set anything aside again")
	}
}

// A new-shape file is not touched at all: no rewrite, no set-aside. The
// guiding rule applies to the config file too — a comment the user left in it
// survives.
func TestLoadLeavesANewShapeConfigAlone(t *testing.T) {
	dir := t.TempDir()
	const yml = "# my node\nid: n0deone\nplugins:\n  - id: pfs0001\n    kind: fs\n    label: files\n"
	path := writeYAML(t, dir, yml)
	if _, err := Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != yml {
		t.Errorf("a new-shape file was rewritten:\n%s", after)
	}
	if _, err := os.Stat(path + ".pre-one-node"); !os.IsNotExist(err) {
		t.Error("a new-shape file must not be set aside")
	}
}

// The home row's own durable config key — the login shell — becomes `shell:`,
// and a legacy transport row is dropped (the node builds its transport from
// `connections:` now).
func TestLoadConvertsShellAndDropsTheTransportRow(t *testing.T) {
	dir := t.TempDir()
	path := writeYAML(t, dir, `node_id: 8aed3340244e2053890889c4759cd373
bind: 127.0.0.1:9999
plugins:
    - id: h0meid1
      name: local
      kind: localdb
      config:
        shell: /bin/zsh
    - id: r3mote1
      name: remote
      kind: ssh
connections:
    - name: geneva
      host: geneva.example
      addr: /home/j/.gridwell/federation.sock
retired_names:
    - olddead
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ID != "h0meid1" || cfg.Shell != "/bin/zsh" {
		t.Fatalf("cfg = id %q shell %q", cfg.ID, cfg.Shell)
	}
	if len(cfg.Plugins) != 0 {
		t.Fatalf("plugins = %+v, want none (home and the transport are the node)", cfg.Plugins)
	}
	if len(cfg.Connections) != 1 || cfg.Connections[0].Name != "geneva" || cfg.Connections[0].Host != "geneva.example" {
		t.Fatalf("connections = %+v, want the declared row carried over", cfg.Connections)
	}
	if !cfg.Web.BindSet || cfg.Web.Bind != "127.0.0.1:9999" {
		t.Fatalf("web = %+v, want the flat legacy bind folded into the web door", cfg.Web)
	}
	if len(cfg.RetiredNames) != 1 || cfg.RetiredNames[0] != "olddead" {
		t.Fatalf("retired names = %v", cfg.RetiredNames)
	}
}

// The conversion refuses rather than guesses, and refusing leaves the file
// exactly as the user left it: nothing set aside, nothing rewritten.
func TestLoadRefusesAnAmbiguousLegacyConfig(t *testing.T) {
	cases := map[string]struct{ yml, want string }{
		"no home row": {
			// Nothing here says which id the home store is under, and minting
			// a fresh one would orphan every stored reference.
			yml:  "node_id: 8aed3340244e2053890889c4759cd373\nplugins:\n  - id: pfs0001\n    name: files\n    kind: fs\n",
			want: "no home row",
		},
		"two home rows": {
			yml:  "plugins:\n  - id: h0meid1\n    name: \"\"\n    kind: home\n  - id: h0meid2\n    name: \"\"\n    kind: local\n",
			want: "two home rows",
		},
		"a row with no id": {
			yml:  "node_id: 8aed3340244e2053890889c4759cd373\nplugins:\n  - name: files\n    kind: fs\n",
			want: "no id",
		},
		"an unknown key": {
			yml:  "node_id: 8aed3340244e2053890889c4759cd373\nmounts:\n  - name: old\nplugins:\n  - id: h0meid1\n    name: \"\"\n    kind: home\n",
			want: "never seen",
		},
		"a transport row with connection keys": {
			yml:  "plugins:\n  - id: h0meid1\n    name: \"\"\n    kind: home\n  - id: r3mote1\n    name: geneva\n    kind: ssh\n    config:\n      host: geneva.example\n",
			want: "no home in the one-node config",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeYAML(t, dir, tc.yml)
			_, err := Load(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want a refusal naming %q", err, tc.want)
			}
			after, rerr := os.ReadFile(path)
			if rerr != nil || string(after) != tc.yml {
				t.Errorf("a refused conversion must leave the file untouched: %v\n%s", rerr, after)
			}
			if _, serr := os.Stat(path + ".pre-one-node"); !os.IsNotExist(serr) {
				t.Error("a refused conversion must set nothing aside")
			}
		})
	}
}

// An earlier conversion's original is never overwritten: the file the user's
// data was built from stays as it is, and the second conversion says so.
func TestConversionNeverOverwritesAnEarlierOriginal(t *testing.T) {
	dir := t.TempDir()
	path := writeYAML(t, dir, laptopYAML)
	if err := os.WriteFile(path+".pre-one-node", []byte("# an earlier original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v, want a refusal naming the existing set-aside file", err)
	}
	aside, err := os.ReadFile(path + ".pre-one-node")
	if err != nil || string(aside) != "# an earlier original\n" {
		t.Fatalf("the earlier original must survive: %q (%v)", aside, err)
	}
}
