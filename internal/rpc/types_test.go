package rpc

import "testing"

func TestIsWellKind(t *testing.T) {
	wells := []string{KindWell, KindFileWell, KindProcessWell}
	for _, k := range wells {
		if !IsWellKind(k) {
			t.Errorf("IsWellKind(%q) = false, want true", k)
		}
	}
	notWells := []string{KindText, KindURL, KindShell, ""}
	for _, k := range notWells {
		if IsWellKind(k) {
			t.Errorf("IsWellKind(%q) = true, want false", k)
		}
	}
}

// TestIsContentDescentKind pins the content-descent set the click router and
// the URL-restore walk both rely on. Shell MUST be included (it sets TextFocus
// and is encoded into the URL, so it has to round-trip on reload); the well
// kinds must NOT be (they are grid descents, not text-focus). This is the drift
// that dropped shell descents on reload.
func TestIsContentDescentKind(t *testing.T) {
	content := []string{KindText, KindURL, KindShell}
	for _, k := range content {
		if !IsContentDescentKind(k) {
			t.Errorf("IsContentDescentKind(%q) = false, want true", k)
		}
	}
	notContent := []string{KindWell, KindFileWell, KindProcessWell, ""}
	for _, k := range notContent {
		if IsContentDescentKind(k) {
			t.Errorf("IsContentDescentKind(%q) = true, want false", k)
		}
	}
}
