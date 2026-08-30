// Package web embeds the browser client into the gridwell binary: index.html,
// the wasm binary and its gzip sidecar, wasm_exec.js, and the vendored xterm
// assets. The distributed binaries are then self-contained; copy gridwell and
// each gridwell-plugin-<kind> to any machine and the web client serves from the
// binary itself, with no files beside it.
//
// The embed list is explicit on purpose. gridwell.wasm and wasm_exec.js are
// build artifacts, and naming them here makes a `go build` without them a
// compile error instead of a binary that serves 404s; the Makefile builds `wasm`
// before `bin` for that reason. server.yaml's `static:`, or --static, is the dev
// override that serves from disk instead.
package web

import "embed"

//go:embed index.html wasm_exec.js gridwell.wasm gridwell.wasm.gz vendor
var FS embed.FS
