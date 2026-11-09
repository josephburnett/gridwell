package rpc

import "testing"

func TestStampSize(t *testing.T) {
	if n, ok := StampSize(ContentStamp(1794193844311182665, 1234567)); !ok || n != 1234567 {
		t.Errorf("StampSize = %d, %v", n, ok)
	}
	for _, bad := range []string{"", "12", "12-x", "12--3"} {
		if _, ok := StampSize(bad); ok {
			t.Errorf("StampSize(%q) is ok", bad)
		}
	}
}

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
