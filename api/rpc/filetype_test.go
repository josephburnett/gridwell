package rpc

import "testing"

func TestExtensionType(t *testing.T) {
	for name, want := range map[string]string{
		"archive.tar.gz": "gzip",
		"FIRMWARE.BIN":   "binary",
		"thing.xyz":      "xyz",
		"Makefile":       "file",
		".bashrc":        "file",
		"trailing.":      "file",
	} {
		if got := ExtensionType(name); got != want {
			t.Errorf("ExtensionType(%q) = %q, want %q", name, got, want)
		}
	}
}
