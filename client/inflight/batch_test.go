package inflight

import (
	"slices"
	"testing"
)

func TestBatchHoldsAPaintsAsksUntilItEnds(t *testing.T) {
	var b Batch[string]
	if got := b.Add("a"); !slices.Equal(got, []string{"a"}) {
		t.Errorf("an ask outside a paint reads at once: %v", got)
	}
	b.Begin()
	if got := b.Add("b"); got != nil {
		t.Errorf("an ask inside a paint waits for its end: %v", got)
	}
	b.Begin()
	b.Add("c")
	if got := b.End(); got != nil {
		t.Errorf("a nested paint's end reads nothing: %v", got)
	}
	b.Add("d")
	if got := b.End(); !slices.Equal(got, []string{"b", "c", "d"}) {
		t.Errorf("the outermost end reads every ask since its begin, in order: %v", got)
	}
	b.Begin()
	if got := b.End(); got != nil {
		t.Errorf("a paint that asked nothing reads nothing: %v", got)
	}
	b.Begin()
	if got := b.Add("e"); got != nil {
		t.Errorf("a paint after the batch left starts empty: %v", got)
	}
	if got := b.End(); !slices.Equal(got, []string{"e"}) {
		t.Errorf("and reads only its own asks: %v", got)
	}
}
