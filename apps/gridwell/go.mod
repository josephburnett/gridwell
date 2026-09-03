module github.com/josephburnett/gridwell/apps/gridwell

go 1.26.6

// A LEAF COMPOSER (docs/plugin.md): replaces every in-repo sibling —
// replace directives only apply in the main module, so each leaf
// repeats the set.

replace (
	github.com/josephburnett/gridwell => ../..
	github.com/josephburnett/gridwell/api => ../../api
	github.com/josephburnett/gridwell/internal/doctype => ../../internal/doctype
)

require github.com/josephburnett/gridwell v0.0.0-00010101000000-000000000000

require (
	connectrpc.com/connect v1.20.0 // indirect
	github.com/coder/websocket v1.8.15 // indirect
	github.com/creack/pty v1.1.24 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/fatih/color v1.19.0 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hashicorp/go-hclog v1.6.3 // indirect
	github.com/hashicorp/go-plugin v1.8.0 // indirect
	github.com/hashicorp/yamux v0.1.2 // indirect
	github.com/josephburnett/gridwell/api v0.0.0-00010101000000-000000000000 // indirect
	github.com/josephburnett/gridwell/internal/doctype v0.0.0-00010101000000-000000000000 // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/oklog/run v1.2.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/yuin/goldmark v1.8.6 // indirect
	golang.org/x/crypto v0.56.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260831171406-18b4a7587f8a // indirect
	google.golang.org/grpc v1.83.2 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	modernc.org/libc v1.75.7 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
	modernc.org/sqlite v1.58.0 // indirect
)
