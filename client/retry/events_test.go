package retry

import (
	"context"
	"errors"
	"slices"
	"testing"

	pb "github.com/josephburnett/gridwell/api/gen/gridwell/v1"
)

// script is one stream's life: the events it delivers, then how it ends.
type script struct {
	refuse bool
	events int
	err    error
}

type scripted struct {
	s    script
	sent int
}

func (f *scripted) Recv() (*pb.Event, bool, error) {
	if f.sent < f.s.events {
		f.sent++
		return &pb.Event{}, true, nil
	}
	if f.s.err != nil {
		return nil, false, f.s.err
	}
	return nil, false, nil
}

func (f *scripted) Close() error { return nil }

// The loop's record of a scripted run: every open says whether it owes the
// resync, and comes before the events of its stream; every stream that
// opened says how it ended, a clean end included.
func TestEventsOpensBeforeEventsAndResyncsAfterEveryGap(t *testing.T) {
	broke := errors.New("network error")
	plan := []script{
		{events: 1, err: broke}, // the first stream, then a break
		{refuse: true},          // the node is unreachable
		{events: 2},             // a clean end is a gap too
		{events: 0},             // a silent stream still opens
	}
	ctx, cancel := context.WithCancel(context.Background())
	var log []string
	i := 0
	Events{
		Subscribe: func(context.Context) (Stream, error) {
			if i == len(plan) {
				cancel()
				return nil, context.Canceled
			}
			s := plan[i]
			i++
			if s.refuse {
				return nil, broke
			}
			return &scripted{s: s}, nil
		},
		Opened: func(resync bool) {
			if resync {
				log = append(log, "open+resync")
			} else {
				log = append(log, "open")
			}
		},
		Ended: func(err error) {
			if err != nil {
				log = append(log, "end:"+err.Error())
			} else {
				log = append(log, "end:clean")
			}
		},
		Broken: func() { log = append(log, "broken") },
		Event:  func(*pb.Event) { log = append(log, "event") },
	}.Run(ctx)
	want := []string{
		"open", "event", "end:network error", "broken",
		"broken",
		"open+resync", "event", "event", "end:clean",
		"open+resync", "end:clean",
		"broken",
	}
	if !slices.Equal(log, want) {
		t.Fatalf("run = %v\nwant  %v", log, want)
	}
}
