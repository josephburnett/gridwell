package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, yml string) string {
	t.Helper()
	p := filepath.Join(dir, "server.yaml")
	if err := os.WriteFile(p, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad_missing(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "server.yaml"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file must surface fs.ErrNotExist (serve treats it as a fresh home), got %v", err)
	}
}

func TestHome(t *testing.T) {
	t.Setenv("GRIDWELL_HOME", "/x/y")
	if h, _ := Home(); h != "/x/y" {
		t.Errorf("GRIDWELL_HOME must win, got %q", h)
	}
	t.Setenv("GRIDWELL_HOME", "")
	h, err := Home()
	if err != nil || !strings.HasSuffix(h, "/.gridwell") {
		t.Errorf("default home = %q (%v), want ~/.gridwell", h, err)
	}
}

func TestDBPaths(t *testing.T) {
	if got := DBFile("/h"); got != "/h/gridwell.db" {
		t.Errorf("DBFile = %q", got)
	}
	if got := CacheFile("/h"); got != "/h/cache.db" {
		t.Errorf("CacheFile = %q", got)
	}
}

// A fully populated config round-trips through Load.
func TestLoad_full(t *testing.T) {
	p := write(t, t.TempDir(), `id: n0deid1
web:
    bind: "127.0.0.1:10010"
federation:
    socket: /tmp/fed.sock
static: /srv/web
shell: /bin/zsh
disable_shells: true
connections:
    - name: geneva
      label: Geneva
      host: geneva.example
      user: joe
      port: 2222
      addr: /home/joe/.gridwell/federation.sock
      key: /k
      known_hosts: /kh
retired_names: [olddead]
plugins:
    - id: p1
      kind: fs
      label: Home dir
      config:
        root: /home/joe
    - kind: gitlab
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ID != "n0deid1" || cfg.Web.Bind != "127.0.0.1:10010" || !cfg.Web.BindSet ||
		cfg.Federation.Socket != "/tmp/fed.sock" || cfg.StaticDir != "/srv/web" ||
		cfg.Shell != "/bin/zsh" || !cfg.DisableShells {
		t.Fatalf("top level = %+v", cfg)
	}
	if len(cfg.Connections) != 1 || cfg.Connections[0].Name != "geneva" || cfg.Connections[0].Port != 2222 ||
		cfg.Connections[0].Addr != "/home/joe/.gridwell/federation.sock" {
		t.Fatalf("connections = %+v", cfg.Connections)
	}
	if len(cfg.RetiredNames) != 1 || cfg.RetiredNames[0] != "olddead" {
		t.Fatalf("retired = %v", cfg.RetiredNames)
	}
	if len(cfg.Plugins) != 2 || cfg.Plugins[0].ID != "p1" || cfg.Plugins[0].Label != "Home dir" ||
		cfg.Plugins[0].Config["root"] != "/home/joe" || cfg.Plugins[1].Kind != "gitlab" || cfg.Plugins[1].ID != "" {
		t.Fatalf("plugins = %+v", cfg.Plugins)
	}
}

// An empty file is a legal fresh home; defaults fill in.
func TestLoad_empty_and_defaults(t *testing.T) {
	cfg, err := Load(write(t, t.TempDir(), ""))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ID != "" || cfg.Web.Bind != Defaults.Web.Bind || cfg.Web.BindSet || len(cfg.Plugins) != 0 {
		t.Fatalf("fresh config = %+v", cfg)
	}
}

// Every retired shape fails loudly with the fix, never loads silently, and a
// pre-one-database file is refused naming the release that converts it.
func TestLoad_refusesRetiredKeys(t *testing.T) {
	cases := map[string]string{
		"bind: 127.0.0.1:1\n":                    "web",
		"password: hunter2\n":                    "web-password",
		"plugins:\n  - id: p1\n":                 "kind is required",
		"nonsense: 1\n":                          "not found",
		"node_id: n1\n":                          "v0.1.0",
		"plugins:\n  - id: h1\n    kind: home\n": "v0.1.0",
		"plugins:\n  - id: r1\n    kind: ssh\n":  "v0.1.0",
	}
	for yml, want := range cases {
		_, err := Load(write(t, t.TempDir(), yml))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want a refusal mentioning %q", yml, err, want)
		}
	}
}

func TestLoad_rejectsBadIDs(t *testing.T) {
	for _, yml := range []string{
		"id: has/slash\n",
		"id: \"12345\"\n",
		"plugins:\n  - id: \"777\"\n    kind: fs\n",
		"plugins:\n  - id: dup1\n    kind: fs\n  - id: dup1\n    kind: proc\n",
		"connections:\n  - name: \"9\"\n    addr: /s\n",
		"connections:\n  - name: c1\n    addr: /s\n  - name: c1\n    addr: /t\n",
		// A nameless stanza takes the empty segment: ids through it would
		// read as the node's own.
		"connections:\n  - addr: /s\n",
		"connections:\n  - name: \"\"\n    addr: /s\n",
		// A name reserved forever has to be spelled right.
		"retired_names: [\"12\"]\n",
		"retired_names: [\"has/slash\"]\n",
		"retired_names: [\"\"]\n",
		// A name is alive or retired, never both.
		"connections:\n  - name: rtb\n    addr: /s\nretired_names: [rtb]\n",
	} {
		if _, err := Load(write(t, t.TempDir(), yml)); err == nil {
			t.Errorf("%q must be refused", yml)
		}
	}
}

func TestLoad_tilde_expansion(t *testing.T) {
	home, _ := os.UserHomeDir()
	cfg, err := Load(write(t, t.TempDir(), "federation:\n  socket: ~/fed.sock\nstatic: ~/web\nplugins:\n  - id: p1\n    kind: fs\n    binary: ~/bin/x\n    config:\n      root: ~/docs\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Federation.Socket != filepath.Join(home, "fed.sock") || cfg.StaticDir != filepath.Join(home, "web") ||
		cfg.Plugins[0].Binary != filepath.Join(home, "bin/x") || cfg.Plugins[0].Config["root"] != filepath.Join(home, "docs") {
		t.Fatalf("tilde not expanded: %+v", cfg)
	}
}

func TestLoad_invalid_yaml(t *testing.T) {
	if _, err := Load(write(t, t.TempDir(), "plugins: [\n")); err == nil {
		t.Fatal("invalid yaml must be an error")
	}
}

// Mint fills exactly the absent ids and reports it; Save writes 0600 a file
// the next Load reads back, with no derived field in it.
func TestMintAndSave(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "plugins:\n  - kind: fs\n  - id: keep1\n    kind: proc\n")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	cfg.WebPassword, cfg.CacheDir = "secret", "/cache"
	if !Mint(cfg) {
		t.Fatal("Mint must report the minted ids")
	}
	if cfg.ID == "" || cfg.Plugins[0].ID == "" || cfg.Plugins[1].ID != "keep1" {
		t.Fatalf("mint = %+v", cfg)
	}
	if Mint(cfg) {
		t.Fatal("a second Mint must be a no-op")
	}
	if err := Save(p, cfg); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("saved mode = %v, want 0600", st.Mode().Perm())
	}
	raw, _ := os.ReadFile(p)
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "/cache") || strings.Contains(string(raw), "web_password") {
		t.Fatalf("derived fields leaked into the file:\n%s", raw)
	}
	back, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if back.ID != cfg.ID || back.Plugins[0].ID != cfg.Plugins[0].ID || back.Plugins[1].ID != "keep1" {
		t.Fatalf("round trip: %+v vs %+v", back, cfg)
	}
	if _, err := os.Stat(p + ".tmp"); !errors.Is(err, fs.ErrNotExist) {
		t.Error("the temp file must be renamed away")
	}
}

func TestEnsurePasswordFile(t *testing.T) {
	home := t.TempDir()
	pw, err := EnsurePasswordFile(home)
	if err != nil || len(pw) != 32 {
		t.Fatalf("minted %q (%v)", pw, err)
	}
	st, _ := os.Stat(PasswordFile(home))
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", st.Mode().Perm())
	}
	again, _ := EnsurePasswordFile(home)
	if again != pw {
		t.Fatal("the file IS the password: a second read must not re-mint")
	}
	if err := os.Remove(PasswordFile(home)); err != nil {
		t.Fatal(err)
	}
	if rotated, _ := EnsurePasswordFile(home); rotated == pw {
		t.Fatal("deleting the file must rotate")
	}
}

func TestDurableFiles(t *testing.T) {
	got := DurableFiles("/h")
	if len(got) != 2 || got[0] != "/h/server.yaml" || got[1] != "/h/web-password" {
		t.Fatalf("DurableFiles = %v", got)
	}
}

// Save writes the minted ids into the document as it was loaded and nothing
// else: a default the file left absent stays absent, a path stays in the
// user's spelling, and the user's comments stay. Anything more is a config
// write the user did not make, and an explicit web.bind is how the desktop's
// --bind-default stops winning.
func TestSaveWritesOnlyTheMintedIDs(t *testing.T) {
	dir := t.TempDir()
	src := "# my node\nplugins:\n  - kind: fs # files\n    binary: ~/bin/gridwell-plugin-fs\n    config:\n      root: ~/notes\n"
	p := write(t, dir, src)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !Mint(cfg) {
		t.Fatal("expected a mint")
	}
	if err := Save(p, cfg); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	got := string(raw)
	for _, want := range []string{"id: " + cfg.ID, "id: " + cfg.Plugins[0].ID, "~/bin/gridwell-plugin-fs", "~/notes", "# my node", "# files"} {
		if !strings.Contains(got, want) {
			t.Errorf("saved file lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "bind") || strings.Contains(got, "/home/") {
		t.Errorf("saved file carries a derived field:\n%s", got)
	}
	back, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if back.Web.BindSet || back.Web.Bind != Defaults.Web.Bind {
		t.Fatalf("a bind the file never had is now written down: %+v", back.Web)
	}
	if back.ID != cfg.ID || back.Plugins[0].ID != cfg.Plugins[0].ID {
		t.Fatalf("ids did not round-trip: %+v vs %+v", back, cfg)
	}
}

// A fresh home has no file to edit, so the mint writes the id and only the id.
func TestSaveFreshHomeWritesOnlyTheID(t *testing.T) {
	p := filepath.Join(t.TempDir(), "server.yaml")
	fresh := Defaults
	Mint(&fresh)
	if err := Save(p, &fresh); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if string(raw) != "id: "+fresh.ID+"\n" {
		t.Fatalf("fresh save = %q, want the id alone", raw)
	}
}
