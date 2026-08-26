// gridwell-provider-gitlab — the gitlab todos content provider binary:
// a stateless projection of one GitLab account's to-do list, serving
// contentprovider.v1. Config: url (default https://gitlab.com),
// token_file (a host-local file holding a personal access token with
// read_api scope — secrets stay file paths, never yaml values),
// refresh (optional Go duration, default 30s). No database — the node
// owns this external's memory.
package main

import (
	"github.com/josephburnett/gridwell/api/guest"
	"github.com/josephburnett/gridwell/plugins/gitlab/provider"
)

func main() {
	guest.ServeProvider(provider.FromConfig(guest.Config()))
}
