// Package web embeds the browser client into the gridwell binary, so a copied
// binary serves the client with no files beside it. The embed list names the
// build artifacts explicitly: a `go build` without them is then a compile error
// instead of a binary that serves 404s. server.yaml's `static:`, or --static,
// is the dev override that serves from disk instead.
package web

import "embed"

//go:embed index.html wasm_exec.js gridwell.wasm gridwell.wasm.gz vendor
var FS embed.FS
