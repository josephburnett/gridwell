package mcpapi

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/subscription"
)

// screens is a fake node: its sessions, and which one holds shell "k1".
type screens struct {
	all    []subscription.Session
	holder string
	err    error
}

func (s screens) Sessions() []subscription.Session { return s.all }
func (s screens) ShellHolder(_ context.Context, key string) (string, bool, error) {
	if key == "k1" && s.holder != "" {
		return s.holder, true, nil
	}
	return "", false, s.err
}

func open(id string, changed int64) subscription.Session {
	return subscription.Session{ID: id, Open: true, Changed: time.Unix(changed, 0), View: shows()}
}

func away(id string, changed, since int64) subscription.Session {
	return subscription.Session{ID: id, Changed: time.Unix(changed, 0), Away: time.Unix(since, 0), View: shows()}
}

func shows() *pb.SetViewRequest {
	return &pb.SetViewRequest{Panes: []*pb.ViewPane{{Id: "p0", Anchor: "n/1", GridId: "n/1", Focused: true}}}
}

func listed(t *testing.T, s screens, shell string) SessionsOut {
	t.Helper()
	out, err := (&toolset{screens: s}).listSessions(context.Background(), shell)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func ids(out SessionsOut) []string {
	var got []string
	for _, s := range out.Sessions {
		got = append(got, s.ID)
	}
	return got
}

// Every screen is listed, open or away, the newest change first; a view with
// no panes is another node telling its share and shows no screen.
func TestSessionsListsEveryScreenNewestChangeFirst(t *testing.T) {
	node := subscription.Session{ID: "far", Open: true, Changed: time.Unix(900, 0), View: &pb.SetViewRequest{Label: "node"}}
	out := listed(t, screens{all: []subscription.Session{open("desk", 100), away("phone", 300, 400), node}}, "")
	if got := ids(out); !slices.Equal(got, []string{"phone", "desk"}) {
		t.Fatalf("sessions = %v, want phone then desk", got)
	}
	if out.Sessions[0].AwaySince != stamp(time.Unix(400, 0)) || out.Sessions[1].AwaySince != "" {
		t.Errorf("away_since = %q, %q; want the phone's 400 and none for the open desk", out.Sessions[0].AwaySince, out.Sessions[1].AwaySince)
	}
}

// No screen is an empty list, not a refusal.
func TestSessionsWithNoScreenIsEmpty(t *testing.T) {
	out := listed(t, screens{}, "")
	if out.Sessions == nil || len(out.Sessions) != 0 || out.Note != "" {
		t.Fatalf("sessions = %+v, want an empty list", out)
	}
}

// The screen holding the agent's shell live is yours; with no header none
// is, and a shell live nowhere marks none and says so.
func TestSessionsMarksTheScreenHoldingYourShell(t *testing.T) {
	s := screens{all: []subscription.Session{open("desk", 100), open("phone", 300)}, holder: "desk"}
	out := listed(t, s, "k1")
	if !out.Sessions[1].Yours || out.Sessions[0].Yours || out.Note != "" {
		t.Fatalf("sessions = %+v, want desk alone yours", out)
	}
	if out := listed(t, s, ""); slices.ContainsFunc(out.Sessions, func(x Session) bool { return x.Yours }) {
		t.Errorf("with no shell header, sessions = %+v, want none yours", out)
	}
	out = listed(t, s, "k2")
	if slices.ContainsFunc(out.Sessions, func(x Session) bool { return x.Yours }) || !strings.Contains(out.Note, "k2") {
		t.Errorf("a shell live nowhere: %+v, want none yours and a note naming k2", out)
	}
	s.err = errors.New("unreachable")
	if out := listed(t, s, "k2"); !strings.Contains(out.Note, "unreachable") {
		t.Errorf("a failed look-up: note %q, want its reason", out.Note)
	}
}
