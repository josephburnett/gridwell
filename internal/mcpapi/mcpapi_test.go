package mcpapi

import (
	"errors"
	"strings"
	"testing"
)

// A bulk write answers one verdict per item, in order, and a refused item
// stops none after it; an empty list is the call's refusal.
func TestEachAnswersAVerdictPerItemInOrder(t *testing.T) {
	var ran []int
	_, out, err := each([]int{1, 2, 3}, func(i int) (Verdict, error) {
		ran = append(ran, i)
		if i == 2 {
			return Verdict{Deleted: "dropped"}, errors.New("conflict: moved")
		}
		return Verdict{Deleted: "kept"}, nil
	})
	if err != nil || len(ran) != 3 {
		t.Fatalf("each ran %v, err %v; want all three", ran, err)
	}
	got := out.Items
	if got[0].Verdict != "ok" || got[1].Verdict != "conflict: moved" || got[1].Deleted != "" || got[2].Verdict != "ok" {
		t.Fatalf("verdicts = %+v, want ok, the refusal alone, ok", got)
	}
	if _, _, err := each([]int{}, func(int) (Verdict, error) { return Verdict{}, nil }); err == nil || !strings.HasPrefix(err.Error(), "refused:") {
		t.Fatalf("an empty list = %v, want refused", err)
	}
}

// A find range is RFC 3339 at each end, either open, and the wire's unix
// seconds; anything else is refused naming the end.
func TestASpanIsRFC3339AsUnixSeconds(t *testing.T) {
	r, err := (&Span{From: "2026-10-17T00:00:00Z"}).rangeOf("created")
	if err != nil || r.GetFrom() != 1792195200 || r.GetTo() != 0 {
		t.Fatalf("from only = %v, %v; want 1792195200 to open", r, err)
	}
	r, err = (&Span{To: "2026-10-17T02:00:00+02:00"}).rangeOf("created")
	if err != nil || r.GetFrom() != 0 || r.GetTo() != 1792195200 {
		t.Fatalf("to with an offset = %v, %v; want open to 1792195200", r, err)
	}
	if r, err := (*Span)(nil).rangeOf("created"); r != nil || err != nil {
		t.Fatalf("no span = %v, %v; want no range", r, err)
	}
	if _, err := (&Span{To: "1792195200"}).rangeOf("modified"); err == nil || !strings.Contains(err.Error(), "modified.to") {
		t.Fatalf("unix seconds = %v, want refused naming modified.to", err)
	}
}
