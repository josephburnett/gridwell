package server_test

import (
	"context"
	"testing"

	"github.com/josephburnett/gridwell/client/clientsync"
)

// A connection whose far end now lands on another node refuses every write
// through it with its reason, and the client hears a refusal it surfaces,
// never a version race it reloads in silence.
func TestALandingMismatchIsNotAConflictAcrossTheSeam(t *testing.T) {
	ctx := context.Background()
	m := newMesh(t, []string{meNode, aNode, cNode}, []meshEdge{{meNode, "toa", aNode}})
	far := m.text(ctx, meNode, m.home(meNode, aNode, "toa"), 0, "on a")

	m.redeclare(meNode, []meshEdge{{meNode, "toa", cNode}}, nil)
	_, err := m.nodes[meNode].cl.RenameTile(ctx, far.Id, far.Version, "renamed")
	if err == nil {
		t.Fatal("a rename through a connection that lands on another node answered")
	}
	if got := clientsync.Of(err); got != clientsync.OutcomeRejected {
		t.Fatalf("the client reads the landing refusal %q as %v, want rejected", err, got)
	}
}
