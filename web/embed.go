// Package web embeds the browser client — index.html, the wasm binary and
// its gzip sidecar, wasm_exec.js, and the vendored xterm assets — into the
// gridwell binary (2026-08-12), so the distributed binaries are fully
// self-contained: copy gridwell + gridwell-<kind> to any machine and the
// web client serves from the binary itself, no files beside it.
//
// The embed list is EXPLICIT on purpose: gridwell.wasm and wasm_exec.js
// are build artifacts, and naming them here makes a `go build` without
// them a loud compile error instead of a binary that silently serves 404s
// — the Makefile builds `wasm` before `bin` for exactly this reason.
// server.yaml's `static:` (or --static) remains the dev override that
// serves from disk instead.
package web

import "embed"

//go:embed index.html wasm_exec.js gridwell.wasm gridwell.wasm.gz vendor
var FS embed.FS
