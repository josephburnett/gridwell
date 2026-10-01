module github.com/josephburnett/gridwell/apps/gridwell

go 1.27.1

// A LEAF COMPOSER (ARCHITECTURE.md, Module boundaries): replaces every in-repo
// sibling — replace directives only apply in the main module, so each leaf
// repeats the set.

replace (
	github.com/josephburnett/gridwell => ../..
	github.com/josephburnett/gridwell/api => ../../api
	github.com/josephburnett/gridwell/internal/doctype => ../../internal/doctype
)

require github.com/josephburnett/gridwell v0.0.0-00010101000000-000000000000

require (
	connectrpc.com/connect v1.21.0 // indirect
	github.com/coder/websocket v1.8.15 // indirect
	github.com/creack/pty v1.1.24 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
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
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/oklog/run v1.2.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/yuin/goldmark v1.8.6 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260928230214-8a89bd6388cc // indirect
	google.golang.org/grpc v1.84.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	modernc.org/libc v1.77.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
	modernc.org/sqlite v1.60.1 // indirect
)
