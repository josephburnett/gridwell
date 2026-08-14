package doctype

import "testing"

func TestClassification(t *testing.T) {
	for name, want := range map[string]bool{
		"a.md": true, "B.MARKDOWN": true, " notes.org ": true,
		"a.txt": false, "md": false, "a.md.bak": false, "": false,
	} {
		if got := Renderable(name); got != want {
			t.Errorf("Renderable(%q) = %v, want %v", name, got, want)
		}
	}
	if !IsOrg("x.ORG") || IsOrg("x.md") {
		t.Error("IsOrg misclassifies")
	}
}
