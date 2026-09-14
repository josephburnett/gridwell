package cadence

import "testing"

// The one ordering the shim's writers depend on, stated at FramingSaveMs: a
// pan writes its resting framing, and the URL that describes where the user is
// has already settled by then. Equal or inverted values would put the URL
// behind the viewport it names, which no other test would see, because both
// waits are short and every spec eventually polls past them.
func TestFramingSettlesAfterTheURL(t *testing.T) {
	if FramingSaveMs <= URLUpdateMs {
		t.Fatalf("framing settles at %dms, not after the URL's %dms", FramingSaveMs, URLUpdateMs)
	}
}

// A wait of zero or less is not a debounce: setTimeout(0) fires per event, so
// a burst posts once per keystroke or per wheel notch.
func TestEveryCadenceIsAWait(t *testing.T) {
	for _, c := range []struct {
		name string
		ms   int
	}{
		{"TextSaveMs", TextSaveMs},
		{"URLUpdateMs", URLUpdateMs},
		{"FramingSaveMs", FramingSaveMs},
		{"WorkspaceSaveMs", WorkspaceSaveMs},
		{"ShellMirrorMs", ShellMirrorMs},
		{"TraceFadeMs", TraceFadeMs},
	} {
		if c.ms <= 0 {
			t.Errorf("%s is %dms", c.name, c.ms)
		}
	}
}
