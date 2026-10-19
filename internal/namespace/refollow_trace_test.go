package namespace

import (
	"context"
	"testing"
	"time"

	"github.com/josephburnett/gridwell/internal/trace"
)

// An outage and its recovery are one record each, however many retries lie
// between: the log has a line per retry, and nothing else says the stream came
// back.
func TestAStreamsOutageAndRecoveryAreTracedOnce(t *testing.T) {
	const label = "trace stream"
	var mark uint64
	if recs := trace.Default().Snapshot(); len(recs) > 0 {
		mark = recs[len(recs)-1].Seq
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	up := make(chan struct{})
	n := 0
	done := make(chan struct{})
	go func() {
		defer close(done)
		Refollow{
			Label:   label,
			Backoff: Backoff{First: time.Millisecond, Max: time.Millisecond},
			Down:    func(string) {},
			Up:      func() { close(up) },
			Attempt: func(ctx context.Context, established func()) error {
				if n++; n <= 3 {
					return errStreamRefused
				}
				established()
				<-ctx.Done()
				return nil
			},
		}.Run(ctx)
	}()
	select {
	case <-up:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream never came back")
	}
	cancel()
	<-done
	var got []string
	for _, rec := range trace.Default().Snapshot() {
		if rec.Seq > mark && rec.Src == "namespace" && rec.Kind == "stream" && rec.KV["label"] == label {
			got = append(got, rec.Msg)
			if rec.Msg == "down" && rec.KV["reason"] != errStreamRefused.Error() {
				t.Errorf("down record = %v, want the reason", rec)
			}
		}
	}
	if len(got) != 2 || got[0] != "down" || got[1] != "up" {
		t.Errorf("stream records = %v, want [down up]", got)
	}
}
