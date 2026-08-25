package textedit

import "testing"

func TestDecideUnloadFlush(t *testing.T) {
	cases := []struct {
		name               string
		rowKnown, editable bool
		rowVersion, basis  int64
		haveBasis          bool
		wantClaim          int64
		want               UnloadFlush
	}{
		{"cached editable row, basis", true, true, 7, 5, true, 5, UnloadBeacon},
		{"cached editable row, no basis", true, true, 7, 0, false, 7, UnloadBeacon},
		{"cached read-only or non-text row", true, false, 7, 5, true, 0, UnloadSkip},
		// The lost-edit case this rule exists for: a dirty edit whose
		// owner row was never cached (a leaf link's foreign target).
		{"uncached row, basis", false, false, 0, 5, true, 5, UnloadBeacon},
		{"uncached row, no basis", false, false, 0, 0, false, 0, UnloadAsync},
	}
	for _, c := range cases {
		claim, do := DecideUnloadFlush(c.rowKnown, c.editable, c.rowVersion, c.basis, c.haveBasis)
		if claim != c.wantClaim || do != c.want {
			t.Errorf("%s: = (%d, %v), want (%d, %v)", c.name, claim, do, c.wantClaim, c.want)
		}
	}
}
