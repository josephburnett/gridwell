module github.com/josephburnett/gridwell/test/federation

go 1.26.5

// A LEAF COMPOSER (docs/plugin.md): replaces every in-repo sibling —
// replace directives only apply in the main module, so each leaf
// repeats the set.

replace (
	github.com/josephburnett/gridwell => ../..
	github.com/josephburnett/gridwell/api => ../../api
	github.com/josephburnett/gridwell/internal/doctype => ../../internal/doctype
	github.com/josephburnett/gridwell/plugins/fs => ../../plugins/fs
	github.com/josephburnett/gridwell/plugins/griddb => ../../plugins/griddb
	github.com/josephburnett/gridwell/plugins/local => ../../plugins/local
	github.com/josephburnett/gridwell/plugins/proc => ../../plugins/proc
	github.com/josephburnett/gridwell/plugins/remote => ../../plugins/remote
)

require (
	github.com/josephburnett/gridwell/api v0.0.0-00010101000000-000000000000
	github.com/josephburnett/gridwell/plugins/remote v0.0.0-00010101000000-000000000000
)

require (
	connectrpc.com/connect v1.20.0 // indirect
	golang.org/x/crypto v0.51.0 // indirect
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/grpc v1.83.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
