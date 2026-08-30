module github.com/josephburnett/gridwell

go 1.26.5

// The api module lives in this repo (docs/plugin.md): the contract every
// plugin builds against, carved out so the arrows are structural. The
// replace makes the in-repo copy authoritative for the app build.
require (
	github.com/josephburnett/gridwell/api v0.0.0-00010101000000-000000000000
	github.com/josephburnett/gridwell/internal/doctype v0.0.0-00010101000000-000000000000
)

replace (
	github.com/josephburnett/gridwell/api => ./api
	github.com/josephburnett/gridwell/internal/doctype => ./internal/doctype
)

require (
	connectrpc.com/connect v1.20.0
	github.com/creack/pty v1.1.24
	github.com/microcosm-cc/bluemonday v1.0.27
	github.com/niklasfasching/go-org v1.9.1
	github.com/yuin/goldmark v1.8.5
	golang.org/x/crypto v0.55.0
	google.golang.org/grpc v1.83.0
	google.golang.org/protobuf v1.36.12
	gopkg.in/yaml.v3 v3.0.1
	modernc.org/sqlite v1.56.0
)

require (
	github.com/BurntSushi/toml v1.4.1-0.20240526193622-a339e1f7089c // indirect
	github.com/aymerick/douceur v0.2.0 // indirect
	github.com/coder/websocket v1.8.14 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/fatih/color v1.19.0 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/css v1.0.1 // indirect
	github.com/hashicorp/go-hclog v1.6.3 // indirect
	github.com/hashicorp/go-plugin v1.8.0 // indirect
	github.com/hashicorp/yamux v0.1.2 // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/oklog/run v1.2.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/exp/typeparams v0.0.0-20231108232855-2478ac86f678 // indirect
	golang.org/x/mod v0.39.0 // indirect
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/telemetry v0.0.0-20260811182544-a038080d80e5 // indirect
	golang.org/x/text v0.41.0 // indirect
	golang.org/x/tools v0.49.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260807164820-c8921c73eeea // indirect
	honnef.co/go/tools v0.8.1 // indirect
	modernc.org/libc v1.75.3 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.0 // indirect
)

tool (
	golang.org/x/tools/cmd/deadcode
	honnef.co/go/tools/cmd/staticcheck
)
