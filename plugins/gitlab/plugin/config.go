package plugin

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	pluginv1 "github.com/josephburnett/gridwell/api/gen/plugin/v1"
	"github.com/josephburnett/gridwell/plugins/gitlab/gitlabapi"
)

// DefaultURL is the GitLab instance when config names none.
const DefaultURL = "https://gitlab.com"

// FromConfig builds the production plugin from the shared config vocabulary.
// It is the one owner of the config-to-plugin derivation, so the subprocess
// main and a bundled binary compose exactly the same plugin. A missing or
// unreadable token is a refusal: the error is the verdict, and both doors turn
// it into a launch that stops with the reason instead of a plugin serving an
// empty grid.
func FromConfig(cfg map[string]string) (pluginv1.PluginServer, error) {
	base := strings.TrimSpace(cfg["url"])
	if base == "" {
		base = DefaultURL
	}
	var opts Options
	if r := strings.TrimSpace(cfg["refresh"]); r != "" {
		d, err := time.ParseDuration(r)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("gitlab plugin: refresh %q is not a duration (e.g. 30s, 5m)", r)
		}
		opts.Refresh = d
	}
	tokenFile := strings.TrimSpace(cfg["token_file"])
	if tokenFile == "" {
		return nil, errors.New("gitlab plugin: token_file not configured (a file holding a read_api personal access token)")
	}
	raw, err := os.ReadFile(tokenFile)
	if err != nil {
		return nil, fmt.Errorf("gitlab plugin: token_file: %v", err)
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return nil, fmt.Errorf("gitlab plugin: token_file %s is empty", tokenFile)
	}
	return New(gitlabapi.New(base, token, nil), opts), nil
}
