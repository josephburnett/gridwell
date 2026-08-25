module github.com/josephburnett/gridwell/plugins/fs

go 1.26.5

require (
	github.com/josephburnett/gridwell/api v0.0.0-00010101000000-000000000000
	github.com/josephburnett/gridwell/internal/doctype v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.83.0
)

require (
	connectrpc.com/connect v1.20.0 // indirect
	github.com/fatih/color v1.13.0 // indirect
	github.com/golang/protobuf v1.5.4 // indirect
	github.com/hashicorp/go-hclog v1.6.3 // indirect
	github.com/hashicorp/go-plugin v1.8.0 // indirect
	github.com/hashicorp/yamux v0.1.2 // indirect
	github.com/mattn/go-colorable v0.1.12 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/oklog/run v1.1.0 // indirect
	github.com/yuin/goldmark v1.8.5 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260807164820-c8921c73eeea // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace github.com/josephburnett/gridwell/api => ../../api

replace github.com/josephburnett/gridwell/internal/doctype => ../../internal/doctype
