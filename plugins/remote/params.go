package remote

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/josephburnett/gridwell/plugins/remote/dial"
)

// CreateSchemaWell is the #198 creation schema the plugin declares for well
// drops on its root grid: the form the client renders before committing a
// connection. TWO transports, selected by what you fill in (owner decision
// 2026-08-16): an SSH host+user bridges over ssh (auth + encryption — the
// only authenticated transport); an EMPTY host with an addr connects
// DIRECTLY to that gridwell node export — for another node on this
// machine or across the tailnet, where the network itself is the trust
// boundary (the password gates only the web UI, by design). Secrets stay
// host-local FILES (owner decision, #198): key and known_hosts are PATHS
// resolved where this plugin runs — key material never rides tile content
// or the wire. The "secret" format masks input only.
const CreateSchemaWell = `{
  "type": "object",
  "properties": {
    "host":        {"type": "string", "title": "SSH host (leave empty to connect directly)"},
    "user":        {"type": "string", "title": "SSH user (ssh only)"},
    "port":        {"type": "number", "title": "SSH port (default 22)"},
    "addr":        {"type": "string", "title": "Gridwell address — direct: as reachable from THIS host (e.g. 127.0.0.1:8081 or a tailscale IP); ssh: as seen on the remote (default 127.0.0.1:8080)"},
    "key":         {"type": "string", "title": "Key path (ssh only; default ~/.ssh/id_ed25519)", "format": "secret"},
    "known_hosts": {"type": "string", "title": "known_hosts path (ssh only; default ~/.ssh/known_hosts)"}
  },
  "required": []
}`

// Params is a connection's parsed parameter document.
type Params struct {
	Host       string  `json:"host"`
	User       string  `json:"user"`
	Port       float64 `json:"port,omitempty"`
	Addr       string  `json:"addr,omitempty"`
	Key        string  `json:"key,omitempty"`
	KnownHosts string  `json:"known_hosts,omitempty"`
}

// ParseParams is the plugin's AUTHORITATIVE validation of a params document
// (the client's schemaform pre-validation is UX only). Unknown keys are
// tolerated — a newer client may say more than this plugin understands — but
// the known fields are type- and range-checked, and a refusal here surfaces
// on the caller's error strip.
func ParseParams(data []byte) (*Params, error) {
	var p Params
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("connection params: %w", err)
	}
	// The transport selector: an SSH host means the ssh bridge; no host
	// means DIRECT — addr is then the whole story and must be present.
	if strings.TrimSpace(p.Host) == "" {
		if strings.TrimSpace(p.Addr) == "" {
			return nil, fmt.Errorf("connection params: an ssh host, or an addr for a direct connection, is required")
		}
	} else if strings.TrimSpace(p.User) == "" {
		return nil, fmt.Errorf("connection params: user is required for an ssh connection")
	}
	if p.Port != 0 && (p.Port != float64(int64(p.Port)) || p.Port < 1 || p.Port > 65535) {
		return nil, fmt.Errorf("connection params: port must be an integer in 1..65535, got %v", p.Port)
	}
	if strings.Contains(p.Host, "/") {
		return nil, fmt.Errorf("connection params: host must not contain '/'")
	}
	return &p, nil
}

// DialConfig resolves the params to a concrete dial.Config, applying the
// host-side defaults: port 22, addr 127.0.0.1:8080 (the built-in gridwell
// bind), key = the first of ~/.ssh/id_ed25519 / ~/.ssh/id_rsa that exists,
// known_hosts = ~/.ssh/known_hosts. home is the plugin host's home directory
// ("" disables the ~ defaults, forcing explicit paths).
func (p *Params) DialConfig(home string) (dial.Config, error) {
	c := dial.Config{
		User:       p.User,
		KeyPath:    expandHome(p.Key, home),
		KnownHosts: expandHome(p.KnownHosts, home),
		Addr:       p.Addr,
	}
	if p.Host == "" {
		// DIRECT: addr is the node export as reachable from THIS host;
		// no ssh fields resolve or default. Host stays "" — the dialer's
		// transport selector.
		return c, nil
	}
	port := int64(22)
	if p.Port != 0 {
		port = int64(p.Port)
	}
	c.Host = fmt.Sprintf("%s:%d", p.Host, port)
	if c.Addr == "" {
		c.Addr = "127.0.0.1:8080"
	}
	if c.KeyPath == "" {
		if home == "" {
			return dial.Config{}, fmt.Errorf("connection params: key path required (no home directory to default from)")
		}
		c.KeyPath = firstExisting(
			filepath.Join(home, ".ssh", "id_ed25519"),
			filepath.Join(home, ".ssh", "id_rsa"),
		)
	}
	if c.KnownHosts == "" {
		if home == "" {
			return dial.Config{}, fmt.Errorf("connection params: known_hosts path required (no home directory to default from)")
		}
		c.KnownHosts = filepath.Join(home, ".ssh", "known_hosts")
	}
	return c, nil
}

// expandHome expands a leading "~" or "~/" in a user-supplied path against
// home, matching the schema's stated "~/.ssh/id_ed25519"-style UX — the
// auto-defaulted path is already built from home directly (DialConfig
// above); this is the one place a *typed* path gets the same treatment,
// rather than being passed to os.ReadFile verbatim. Leaves p unchanged if it
// has no "~" prefix, or if home is unknown.
func expandHome(p, home string) string {
	if home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// firstExisting returns the first path that exists, or the first path (whose
// open failure will then name the expected default loudly).
func firstExisting(paths ...string) string {
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return paths[0]
}

// autoLabel is the connection's automatic display name, shown until the user
// renames (the alt_user latch): "user@host" once params are set.
func autoLabel(paramsDoc string) string {
	p, err := ParseParams([]byte(paramsDoc))
	if err != nil {
		return ""
	}
	return p.User + "@" + p.Host
}

// CanonicalParams reduces a params document to a comparable form: parsed as
// a JSON object, empty-string values dropped, keys sorted (json.Marshal of
// a map sorts). Two documents that canonicalize equal name THE SAME
// connection — the dedup rule behind the #251 refusal (and the client
// picker's pre-match, which mirrors it as UX).
func CanonicalParams(data []byte) (string, error) {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return "", fmt.Errorf("connection params: %w", err)
	}
	for k, v := range m {
		if s, isStr := v.(string); isStr && s == "" {
			delete(m, k)
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
