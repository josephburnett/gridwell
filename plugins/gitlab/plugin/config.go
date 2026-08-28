package plugin

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/josephburnett/gridwell/plugins/gitlab/gitlabapi"
)

// DefaultURL is the GitLab instance when config names none.
const DefaultURL = "https://gitlab.com"

// FromConfig builds the production provider from the shared config
// vocabulary — the ONE owner of the config→provider derivation, so
// the subprocess main and the bundled binaries (gridwell-all, mobile)
// compose exactly the same provider. A missing or unreadable token is
// not fatal at spawn: the provider answers Info (the plugin is listed)
// and every listing refuses with the reason (charter §6: it surfaces).
func FromConfig(cfg map[string]string) *Provider {
	base := strings.TrimSpace(cfg["url"])
	if base == "" {
		base = DefaultURL
	}
	opts := Options{Label: cfg["label"]}
	if r := strings.TrimSpace(cfg["refresh"]); r != "" {
		if d, err := time.ParseDuration(r); err == nil && d > 0 {
			opts.Refresh = d
		} else {
			return New(nil, fmt.Errorf("refresh %q is not a duration (e.g. 30s, 5m)", r), opts)
		}
	}
	tokenFile := strings.TrimSpace(cfg["token_file"])
	if tokenFile == "" {
		return New(nil, errors.New("token_file not configured (a file holding a read_api personal access token)"), opts)
	}
	raw, err := os.ReadFile(tokenFile)
	if err != nil {
		return New(nil, fmt.Errorf("token_file: %v", err), opts)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return New(nil, fmt.Errorf("token_file %s is empty", tokenFile), opts)
	}
	return New(gitlabapi.New(base, token, nil), nil, opts)
}
