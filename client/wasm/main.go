//go:build js && wasm

// Package main is the WASM entry point for the Ascent client. It is a thin
// shell that wires syscall/js callbacks to the testable subpackages under
// client/.
package main

func main() {
	// Stub: real entry wiring is added in a later commit. For now this lets
	// `make build` succeed end-to-end.
	select {}
}
