package files

import (
	"context"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/api/gwerr"
)

// A grid id the OS cannot read as a path, under a root, is dead, and asking
// for it darkens nothing.
func TestAnIdNamingNoPathIsDeadAndDarkensNothing(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{f.root + "/a\x00b", "rel", ""} {
		_, err := f.s.GetGrid(context.Background(), &pb.GetGridRequest{GridId: idOf(path)})
		if !gwerr.IsDeadLink(err) {
			t.Errorf("GetGrid(%q) = %v, want dead", path, err)
		}
	}
	if h := f.s.health(); h != (health{}) {
		t.Fatalf("health after asking for ids naming no path = %+v, want every directory read", h)
	}
}
