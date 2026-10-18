package mcpdoor

import (
	"context"
	"strings"
	"testing"
	"time"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
	"github.com/josephburnett/gridwell/internal/subscription"
)

type screens []subscription.Session

func (s screens) Sessions() []subscription.Session { return s }
func (screens) ShellHolder(context.Context, string) (string, bool, error) {
	return "", false, nil
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

func chosen(t *testing.T, d *door, named, shell string) (subscription.Session, string, string) {
	t.Helper()
	s, by, note, err := d.choose(context.Background(), d.screensNow(), named, shell)
	if err != nil {
		t.Fatal(err)
	}
	return s, by, note
}

// An open screen wins over an away one, however recently the away one
// changed.
func TestChooseOpenOverAway(t *testing.T) {
	d := &door{screens: screens{away("phone", 300, 400), open("desk", 100)}}
	if s, by, note := chosen(t, d, "", ""); s.ID != "desk" || by != byMostRecent || note != "" {
		t.Errorf("chose %s by %q note %q, want desk by most recent", s.ID, by, note)
	}
}

// With no screen open, the one that went away last is chosen, and the note
// says it is away since when, with or without a shell to follow.
func TestChooseTheLatestAwayWhenNoneIsOpen(t *testing.T) {
	d := &door{screens: screens{away("old", 300, 350), away("phone", 100, 400)}}
	for _, shell := range []string{"", "k1"} {
		s, by, note := chosen(t, d, "", shell)
		if s.ID != "phone" || by != byMostRecent || !strings.Contains(note, "away since "+stamp(time.Unix(400, 0))) {
			t.Errorf("shell %q: chose %s by %q note %q, want phone by most recent, away since 400", shell, s.ID, by, note)
		}
	}
}

// A session named is answered though it is away and another is open.
func TestChooseNamedAway(t *testing.T) {
	d := &door{screens: screens{away("phone", 100, 400), open("desk", 300)}}
	if s, by, _ := chosen(t, d, "phone", ""); s.ID != "phone" || by != byArgument {
		t.Errorf("chose %s by %q, want phone by argument", s.ID, by)
	}
}

// Nothing open and nothing away is no session.
func TestPointedWithNoScreenIsNoSession(t *testing.T) {
	d := &door{screens: screens{}}
	if _, _, err := d.pointed(context.Background(), nil, pointedIn{}); err == nil || !strings.HasPrefix(err.Error(), "no session:") {
		t.Errorf("pointed = %v, want no session", err)
	}
}

// An away screen is marked with when it went away; an open one is not.
func TestSessionOfMarksAway(t *testing.T) {
	if got := sessionOf(away("phone", 100, 400)).AwaySince; got != stamp(time.Unix(400, 0)) {
		t.Errorf("away_since = %q, want 400", got)
	}
	if got := sessionOf(open("desk", 100)).AwaySince; got != "" {
		t.Errorf("open away_since = %q, want empty", got)
	}
}
